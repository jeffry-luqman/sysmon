package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	xcpu "github.com/shirou/gopsutil/v3/cpu"
	xdisk "github.com/shirou/gopsutil/v3/disk"
	xmem "github.com/shirou/gopsutil/v3/mem"
	xnet "github.com/shirou/gopsutil/v3/net"
)

//go:embed index.html apexcharts.js
var staticFiles embed.FS

type SystemStatus struct {
	Timestamp time.Time `json:"timestamp"`
	CPUUsage  []float64 `json:"cpu"`

	MemoryTotal uint64 `json:"mem_total"`
	MemoryUsed  uint64 `json:"mem_used"`
	MemoryFree  uint64 `json:"mem_free"`

	SwapTotal uint64 `json:"swap_total"`
	SwapUsed  uint64 `json:"swap_used"`
	SwapFree  uint64 `json:"swap_free"`

	DiskTotalSpace uint64 `json:"disk_total"`
	DiskUsedSpace  uint64 `json:"disk_used"`
	DiskFreeSpace  uint64 `json:"disk_free"`

	DiskReadBps  uint64 `json:"disk_read"`
	DiskWriteBps uint64 `json:"disk_write"`

	NetRxBps uint64 `json:"net_receive"`
	NetTxBps uint64 `json:"net_send"`
}

const bufferSize = 10

var (
	statusBuffer []SystemStatus
	mu           sync.RWMutex
)

func Get() []SystemStatus {
	mu.RLock()
	defer mu.RUnlock()
	result := make([]SystemStatus, len(statusBuffer))
	copy(result, statusBuffer)
	return result
}

func Collect(interval time.Duration) {
	go collectSystemStatus(interval)
}

func appendStatus(s SystemStatus) {
	mu.Lock()
	defer mu.Unlock()
	if len(statusBuffer) >= bufferSize {
		statusBuffer = statusBuffer[1:]
	}
	statusBuffer = append(statusBuffer, s)
}

func getDiskRootPath() string {
	execPath, err := os.Executable()
	if err != nil {
		if runtime.GOOS == "windows" {
			return "C:\\"
		}
		return "/"
	}
	absPath, err := filepath.Abs(execPath)
	if err != nil {
		if runtime.GOOS == "windows" {
			return "C:\\"
		}
		return "/"
	}

	if runtime.GOOS == "windows" {
		return strings.ToUpper(absPath[:2]) + "\\"
	}
	return "/"
}

func collectSystemStatus(interval time.Duration) {
	var prevDisk map[string]xdisk.IOCountersStat
	var prevNet []xnet.IOCountersStat

	disk1, _ := xdisk.IOCounters()
	net1, _ := xnet.IOCounters(false)
	prevDisk = disk1
	prevNet = net1

	for {
		time.Sleep(interval)

		cpuPercent, _ := xcpu.Percent(0, true)
		vmem, _ := xmem.VirtualMemory()
		swapMem, _ := xmem.SwapMemory()
		diskUsage, _ := xdisk.Usage(getDiskRootPath())

		disk2, _ := xdisk.IOCounters()
		var readBps uint64
		var writeBps uint64
		for name := range prevDisk {
			if d2, ok := disk2[name]; ok {
				readBps += d2.ReadBytes - prevDisk[name].ReadBytes
				writeBps += d2.WriteBytes - prevDisk[name].WriteBytes
			}
		}
		readBps /= uint64(interval.Seconds())
		writeBps /= uint64(interval.Seconds())
		prevDisk = disk2

		net2, _ := xnet.IOCounters(false)
		var rxBps, txBps uint64
		if len(prevNet) > 0 && len(net2) > 0 {
			rxBps = (net2[0].BytesRecv - prevNet[0].BytesRecv) / uint64(interval.Seconds())
			txBps = (net2[0].BytesSent - prevNet[0].BytesSent) / uint64(interval.Seconds())
		}
		prevNet = net2

		appendStatus(SystemStatus{
			Timestamp:      time.Now(),
			CPUUsage:       cpuPercent,
			MemoryTotal:    vmem.Total,
			MemoryUsed:     vmem.Used,
			MemoryFree:     vmem.Free,
			SwapTotal:      swapMem.Total,
			SwapUsed:       swapMem.Used,
			SwapFree:       swapMem.Free,
			DiskTotalSpace: diskUsage.Total,
			DiskUsedSpace:  diskUsage.Used,
			DiskFreeSpace:  diskUsage.Free,
			DiskReadBps:    readBps,
			DiskWriteBps:   writeBps,
			NetRxBps:       rxBps,
			NetTxBps:       txBps,
		})
	}
}

func main() {
	host := flag.String("host", "0.0.0.0:8080", "host:port to listen on")
	cert := flag.String("cert-file", "", "path to TLS certificate (optional)")
	key := flag.String("cert-key-file", "", "path to TLS key (optional)")
	flag.Parse()

	Collect(5 * time.Second)

	// API endpoint
	http.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Get())
	})

	// Embedded static file handler
	fs := http.FileServer(http.FS(staticFiles))
	http.Handle("/", fs)

	listener, err := net.Listen("tcp", *host)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	if *cert != "" && *key != "" {
		fmt.Printf("System monitor running at https://%s\n", *host)
		log.Fatal(http.ServeTLS(listener, nil, *cert, *key))
	} else {
		fmt.Printf("System monitor running at http://%s\n", *host)
		log.Fatal(http.Serve(listener, nil))
	}
}
