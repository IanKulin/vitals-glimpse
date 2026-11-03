package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const jsonVersion = "0.8"
const endPoint = "/vitals"

var memThresholdPercent = 90
var diskThresholdPercent = 80
var cpuThresholdPercent = 90
var diskIOPSThreshold = 400
var tailscaleExpiryThreshold = 7
var port = 10321

var iodev string
var selectedIODev string

var netIface string
var selectedNetIface string
var windowSeconds = 30
var useCgroupCPU bool

var bindAddr = "0.0.0.0"
var apiKey string
var allowCIDRs string
var allowedNets []*net.IPNet
var rateLimit int

var runningInContainer bool
var tailscalePresent bool
var hostname string

type rateLimiter struct {
	mu        sync.Mutex
	counts    map[string]int
	lastClean int64
}

var limiter = &rateLimiter{counts: make(map[string]int)}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now().Unix() / 60
	key := ip + "|" + strconv.FormatInt(now, 10)

	if now != rl.lastClean {
		suffix := "|" + strconv.FormatInt(now, 10)
		for k := range rl.counts {
			if !strings.HasSuffix(k, suffix) {
				delete(rl.counts, k)
			}
		}
		rl.lastClean = now
	}

	rl.counts[key]++
	return rl.counts[key] <= rateLimit
}

func requireKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if apiKey != "" {
			provided := r.Header.Get("X-API-Key")
			if subtle.ConstantTimeCompare([]byte(provided), []byte(apiKey)) != 1 {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

func requireAllowedIP(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(allowedNets) > 0 {
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			ip := net.ParseIP(host)
			allowed := false
			for _, n := range allowedNets {
				if n.Contains(ip) {
					allowed = true
					break
				}
			}
			if !allowed {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

func isAllowedIP(r *http.Request) bool {
	if len(allowedNets) == 0 {
		return false
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	for _, n := range allowedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func rateCheck(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rateLimit > 0 && !isAllowedIP(r) {
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			if !limiter.allow(host) {
				http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
				return
			}
		}
		next(w, r)
	}
}


func serveStats(resp http.ResponseWriter, req *http.Request) {
    io.WriteString(resp, statusAsJson())
}


func handleRequests() {
	handler := requireAllowedIP(rateCheck(requireKey(serveStats)))
	http.HandleFunc(endPoint, handler)
	http.HandleFunc("/", handler)

	addr := fmt.Sprintf("%s:%d", bindAddr, port)
	log.Printf("vitals-glimpse v%s listening on %s", jsonVersion, addr)
	if apiKey != "" {
		log.Println("  API key: required")
	}
	if len(allowedNets) > 0 {
		cidrs := make([]string, len(allowedNets))
		for i, n := range allowedNets {
			cidrs[i] = n.String()
		}
		log.Printf("  Allowed CIDRs: %s", strings.Join(cidrs, ", "))
	}
	if rateLimit > 0 {
		log.Printf("  Rate limit: %d req/min per IP", rateLimit)
	}
	if selectedIODev != "" {
		log.Printf("  Disk IOPS device: %s", selectedIODev)
	} else {
		log.Println("  Disk IOPS: no device found")
	}
	log.Printf("  Rate averaging window: %ds", windowSeconds)
	if selectedNetIface != "" {
		log.Printf("  Network interface: %s", selectedNetIface)
	} else {
		log.Println("  Network: no interface found")
	}
	if tailscalePresent {
		log.Printf("  Tailscale expiry threshold: %d days", tailscaleExpiryThreshold)
	} else {
		log.Println("  Tailscale: not detected, expiry skipped")
	}

	server := &http.Server{
		Addr:         addr,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}

func main() {
	flag.IntVar(&memThresholdPercent, "mem", 90, "memory usage threshold percent")
	flag.IntVar(&diskThresholdPercent, "disk", 80, "disk usage threshold percent")
	flag.IntVar(&cpuThresholdPercent, "cpu", 90, "cpu usage threshold percent")
	flag.IntVar(&port, "port", 10321, "server port")
	flag.StringVar(&bindAddr, "bind", "0.0.0.0", "address to bind to")
	flag.StringVar(&apiKey, "key", "", "API key required via X-API-Key header")
	flag.StringVar(&allowCIDRs, "allow", "", "comma-separated CIDR allowlist (e.g. \"10.0.0.0/24,192.168.1.0/24\")")
	flag.IntVar(&rateLimit, "ratelimit", 60, "max requests per IP per minute (0 to disable)")
	flag.IntVar(&diskIOPSThreshold, "disk_iops", 400, "IOPS threshold for pass/fail")
	flag.IntVar(&tailscaleExpiryThreshold, "ts_expiry", 7, "days-until-Tailscale-key-expiry threshold for pass/fail")
	flag.StringVar(&iodev, "iodev", "", "device name to measure (e.g. sda). Auto-detected if empty.")
	flag.StringVar(&netIface, "iface", "", "network interface to measure (e.g. eth0). Auto-detected from default route if empty.")
	flag.IntVar(&windowSeconds, "window", 30, "averaging window in seconds for rate metrics (multiple of 5, 5-60)")
	flag.Parse()

	if memThresholdPercent < 1 || memThresholdPercent > 100 {
		log.Fatalf("invalid -mem value %d: must be between 1 and 100", memThresholdPercent)
	}
	if diskThresholdPercent < 1 || diskThresholdPercent > 100 {
		log.Fatalf("invalid -disk value %d: must be between 1 and 100", diskThresholdPercent)
	}
	if cpuThresholdPercent < 1 || cpuThresholdPercent > 100 {
		log.Fatalf("invalid -cpu value %d: must be between 1 and 100", cpuThresholdPercent)
	}
	if port < 1 || port > 65535 {
		log.Fatalf("invalid -port value %d: must be between 1 and 65535", port)
	}
	if rateLimit < 0 {
		log.Fatalf("invalid -ratelimit value %d: must be >= 0", rateLimit)
	}
	if diskIOPSThreshold < 1 || diskIOPSThreshold > 10000000 {
		log.Fatalf("invalid -disk_iops value %d: must be between 1 and 10000000", diskIOPSThreshold)
	}
	if tailscaleExpiryThreshold < 1 || tailscaleExpiryThreshold > 365 {
		log.Fatalf("invalid -ts_expiry value %d: must be between 1 and 365", tailscaleExpiryThreshold)
	}
	if !validWindow(windowSeconds) {
		log.Fatalf("invalid -window value %d: must be a multiple of 5 between 5 and 60", windowSeconds)
	}

	if allowCIDRs != "" {
		for _, cidr := range strings.Split(allowCIDRs, ",") {
			cidr = strings.TrimSpace(cidr)
			if !strings.Contains(cidr, "/") {
				if strings.Contains(cidr, ":") {
					cidr += "/128"
				} else {
					cidr += "/32"
				}
			}
			_, network, err := net.ParseCIDR(cidr)
			if err != nil {
				log.Fatalf("invalid -allow CIDR %q: %v", cidr, err)
			}
			allowedNets = append(allowedNets, network)
		}
	}

	runningInContainer = isInContainer()
	tailscalePresent = tailscaleSocketExists()
	hostname = getHostname()

	if iodev != "" {
		selectedIODev = iodev
	} else {
		selectedIODev = findPrimaryDisk()
	}

	if netIface != "" {
		selectedNetIface = netIface
	} else {
		selectedNetIface = findDefaultRouteIface()
	}
	if selectedNetIface != "" {
		if _, _, err := readNetStats(selectedNetIface); err != nil {
			log.Printf("network warning: %v", err)
			selectedNetIface = ""
		}
	}

	useCgroupCPU = runningInContainer && hasCgroupCPUStats()

	startSampler()
	handleRequests()
}


func statusAsJson() string {
	percentMemUsed := percentMemUsed()
	percentDiskUsed := percentDiskUsed()

	cpuResult, iopsResult, netIn, netOut := -1, -1, -1, -1
	if oldest, newest, ok := sampler.window(); ok {
		if useCgroupCPU {
			cpuResult = cpuPercentCgroup(oldest, newest)
		} else {
			cpuResult = cpuPercentProcStat(oldest, newest)
		}
		iopsResult = iops(oldest, newest)
		netIn, netOut = netKBs(oldest, newest)
	}
	if cpuResult < 0 {
		cpuResult = 0
	}
	tsDay, tsEnabled, tsAvailable := tailscaleExpiryDays()

	returnString := "{\"title\":\"vitals-glimpse\",\"version\":" + jsonVersion + ",\"hostname\":\"" + hostname + "\","

	if percentMemUsed < memThresholdPercent {
		returnString += "\"mem_status\":\"mem_okay\",\"mem_percent\":"
	} else {
		returnString += "\"mem_status\":\"mem_fail\",\"mem_percent\":"
	}
	returnString += fmt.Sprintf("%d,", percentMemUsed)

	if percentDiskUsed < diskThresholdPercent {
		returnString += "\"disk_status\":\"disk_okay\",\"disk_percent\":"
	} else {
		returnString += "\"disk_status\":\"disk_fail\",\"disk_percent\":"
	}
	returnString += fmt.Sprintf("%d,", percentDiskUsed)

	if cpuResult < cpuThresholdPercent {
		returnString += "\"cpu_status\":\"cpu_okay\",\"cpu_percent\":"
	} else {
		returnString += "\"cpu_status\":\"cpu_fail\",\"cpu_percent\":"
	}
	returnString += fmt.Sprintf("%d", cpuResult)

	if iopsResult >= 0 {
		if iopsResult < diskIOPSThreshold {
			returnString += ",\"disk_iops_status\":\"disk_iops_okay\",\"disk_iops\":"
		} else {
			returnString += ",\"disk_iops_status\":\"disk_iops_fail\",\"disk_iops\":"
		}
		returnString += fmt.Sprintf("%d", iopsResult)
	}

	if netIn >= 0 && netOut >= 0 {
		returnString += fmt.Sprintf(",\"net_in_kbs\":%d,\"net_out_kbs\":%d", netIn, netOut)
	}

	if tsAvailable {
		if !tsEnabled {
			returnString += ",\"ts_expiry_status\":\"ts_expiry_okay\""
		} else if tsDay > tailscaleExpiryThreshold {
			returnString += fmt.Sprintf(",\"ts_expiry_status\":\"ts_expiry_okay\",\"ts_expiry_days\":%d", tsDay)
		} else {
			returnString += fmt.Sprintf(",\"ts_expiry_status\":\"ts_expiry_fail\",\"ts_expiry_days\":%d", tsDay)
		}
	}
	returnString += "}"

	return returnString
}


func getHostname() string {
	if data, err := os.ReadFile("/etc/hostname"); err == nil {
		if h := strings.TrimSpace(string(data)); h != "" {
			return h
		}
	}
	h, err := os.Hostname()
	if err != nil {
		log.Println("Warning: could not determine hostname:", err)
		return "unknown"
	}
	return h
}


func parseInt(s string) int {
	var value int
	fmt.Sscanf(s, "%d", &value)
	return value
}


func percentMemUsed() int {
	memInfo, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		log.Println("Error reading /proc/meminfo:", err)
		return -1
	}

	memInfoLines := strings.Split(string(memInfo), "\n")
	memStats := make(map[string]int)

	for _, line := range memInfoLines {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				key := parts[0]
				value := parts[1]
				memStats[key] = parseInt(value)
			}
	}

	memAvail, ok := memStats["MemAvailable:"]
	if !ok || memAvail == 0 {
		// MemAvailable absent (kernel < 3.14); approximate with MemFree + Buffers + Cached
		memAvail = memStats["MemFree:"] + memStats["Buffers:"] + memStats["Cached:"]
	}
	percentAvail := float32(memAvail) * 100 / float32(memStats["MemTotal:"])
	return 99 - int(percentAvail)
}


func percentDiskUsed() int {
	var stat syscall.Statfs_t

	err := syscall.Statfs("/", &stat)
	if err != nil {
		log.Println("Error fetching Statfs for '/'", err)
		return -1
	}

	totalBlocks := stat.Blocks
	availableBlocks := stat.Bavail

	var totalSpace = int(totalBlocks)
	if totalSpace == 0 {
		log.Println("totalSpace unexpectedly zero")
		totalSpace = -1
	}
	availableSpace := int(availableBlocks)

	return 99-int(availableSpace*100/totalSpace)
}


func findPrimaryDisk() string {
	data, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		name := fields[2]
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") {
			continue
		}
		if _, err := os.Stat("/sys/block/" + name); err != nil {
			continue
		}
		return name
	}
	return ""
}

func readDiskStats(device string) (reads, writes int64, err error) {
	data, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		if fields[2] != device {
			continue
		}
		reads, err = strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			return 0, 0, err
		}
		writes, err = strconv.ParseInt(fields[7], 10, 64)
		if err != nil {
			return 0, 0, err
		}
		return reads, writes, nil
	}
	return 0, 0, fmt.Errorf("device %q not found in /proc/diskstats", device)
}


func parseCPUFields(fields []string) (user, nice, system, idle, iowait, irq, softirq, steal int) {
	user, _ = strconv.Atoi(fields[1])
	nice, _ = strconv.Atoi(fields[2])
	system, _ = strconv.Atoi(fields[3])
	idle, _ = strconv.Atoi(fields[4])
	iowait, _ = strconv.Atoi(fields[5])
	irq, _ = strconv.Atoi(fields[6])
	softirq, _ = strconv.Atoi(fields[7])
	steal, _ = strconv.Atoi(fields[8])
	return
}

func getCPUTimes() (idleTime, totalTime int) {
	contents, err := os.ReadFile("/proc/stat")
	if err != nil {
		log.Println("Error reading /proc/stat:", err)
		return
	}

	lines := strings.Split(string(contents), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "cpu" {
			user, nice, system, idle, iowait, irq, softirq, steal := parseCPUFields(fields)
			idleTime = idle + iowait
			totalTime = user + nice + system + idle + iowait + irq + softirq + steal
			return
		}
	}
	return
}

func isInContainer() bool {
	// systemd reliably sets this inside containers
	if data, err := os.ReadFile("/run/systemd/container"); err == nil {
		val := strings.TrimSpace(string(data))
		if val == "lxc" || val == "docker" || val == "oci" {
			return true
		}
	}

	// Docker-specific marker file
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}

	// Check cgroup v2 cpu limits
	if data, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		line := strings.TrimSpace(string(data))
		if line != "max 100000" && !strings.HasPrefix(line, "max ") {
			return true
		}
	}

	// Fallback: cgroup v1 paths
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		content := string(data)
		if strings.Contains(content, "/lxc/") ||
			strings.Contains(content, "/docker/") {
			return true
		}
	}

	return false
}

func getCGroupCPUUsage() (int64, error) {
	contents, err := os.ReadFile("/sys/fs/cgroup/cpu.stat")
	if err != nil {
		return 0, err
	}

	for _, line := range strings.Split(string(contents), "\n") {
		if strings.HasPrefix(line, "usage_usec") {
			fields := strings.Fields(line)
			if len(fields) == 2 {
				return strconv.ParseInt(fields[1], 10, 64)
			}
		}
	}
	return 0, nil
}

func hasCgroupCPUStats() bool {
	_, err := os.ReadFile("/sys/fs/cgroup/cpu.stat")
	return err == nil
}

// Network

func findDefaultRouteIface() string {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	best := ""
	bestMetric := -1
	for i, line := range strings.Split(string(data), "\n") {
		if i == 0 {
			continue // header
		}
		fields := strings.Fields(line)
		if len(fields) < 7 || fields[1] != "00000000" {
			continue
		}
		metric, err := strconv.Atoi(fields[6])
		if err != nil {
			continue
		}
		if bestMetric < 0 || metric < bestMetric {
			best, bestMetric = fields[0], metric
		}
	}
	return best
}

func readNetStats(iface string) (rx, tx uint64, err error) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(name) != iface {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			return 0, 0, fmt.Errorf("unexpected /proc/net/dev format for %q", iface)
		}
		rx, err = strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0, 0, err
		}
		tx, err = strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			return 0, 0, err
		}
		return rx, tx, nil
	}
	return 0, 0, fmt.Errorf("interface %q not found in /proc/net/dev", iface)
}

// Background sampling

const sampleInterval = 5 * time.Second

type sample struct {
	t                 time.Time
	cpuOK             bool
	cpuIdle, cpuTotal int    // /proc/stat
	cgroupUsec        int64  // cpu.stat usage_usec
	diskOK            bool
	reads, writes     int64  // /proc/diskstats
	netOK             bool
	rxBytes, txBytes  uint64 // /proc/net/dev
}

type sampleHistory struct {
	mu      sync.Mutex
	samples []sample
}

var sampler = &sampleHistory{}

func validWindow(w int) bool {
	return w >= 5 && w <= 60 && w%5 == 0
}

func takeSample() sample {
	s := sample{t: time.Now()}

	if useCgroupCPU {
		if usec, err := getCGroupCPUUsage(); err == nil {
			s.cpuOK, s.cgroupUsec = true, usec
		}
	} else {
		// getCPUTimes returns zeros on a failed read
		if idle, total := getCPUTimes(); total > 0 {
			s.cpuOK, s.cpuIdle, s.cpuTotal = true, idle, total
		}
	}

	if !runningInContainer && selectedIODev != "" {
		if r, w, err := readDiskStats(selectedIODev); err == nil {
			s.diskOK, s.reads, s.writes = true, r, w
		}
	}

	if selectedNetIface != "" {
		if rx, tx, err := readNetStats(selectedNetIface); err == nil {
			s.netOK, s.rxBytes, s.txBytes = true, rx, tx
		}
	}
	return s
}

func (h *sampleHistory) add(s sample, keep int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.samples = append(h.samples, s)
	if len(h.samples) > keep {
		h.samples = h.samples[1:]
	}
}

// pickWindow returns the newest sample and the one n ticks before it,
// clamped to the oldest sample held. ok is false with fewer than two samples.
func pickWindow(samples []sample, n int) (oldest, newest sample, ok bool) {
	if len(samples) < 2 {
		return sample{}, sample{}, false
	}
	last := len(samples) - 1
	return samples[last-min(n, last)], samples[last], true
}

func (h *sampleHistory) window() (oldest, newest sample, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return pickWindow(h.samples, windowSeconds/int(sampleInterval/time.Second))
}

func startSampler() {
	keep := windowSeconds/int(sampleInterval/time.Second) + 1
	sampler.add(takeSample(), keep)
	time.Sleep(1 * time.Second)
	sampler.add(takeSample(), keep)

	go func() {
		ticker := time.NewTicker(sampleInterval)
		for range ticker.C {
			sampler.add(takeSample(), keep)
		}
	}()
}

// Rate functions. All return -1 when the rate can't be computed.

func cpuPercentProcStat(a, b sample) int {
	if !a.cpuOK || !b.cpuOK || !b.t.After(a.t) {
		return -1
	}
	if b.cpuTotal < a.cpuTotal || b.cpuIdle < a.cpuIdle {
		return -1
	}
	totalDelta := b.cpuTotal - a.cpuTotal
	if totalDelta == 0 {
		return 0
	}
	idleDelta := b.cpuIdle - a.cpuIdle
	return int(100 * float64(totalDelta-idleDelta) / float64(totalDelta))
}

func cpuPercentCgroup(a, b sample) int {
	elapsed := b.t.Sub(a.t)
	if !a.cpuOK || !b.cpuOK || elapsed <= 0 || b.cgroupUsec < a.cgroupUsec {
		return -1
	}
	elapsedUsec := float64(elapsed.Microseconds())
	usage := int(100 * float64(b.cgroupUsec-a.cgroupUsec) / (elapsedUsec * float64(runtime.NumCPU())))
	if usage > 100 {
		usage = 100
	}
	return usage
}

func iops(a, b sample) int {
	elapsed := b.t.Sub(a.t).Seconds()
	if !a.diskOK || !b.diskOK || elapsed <= 0 || b.reads < a.reads || b.writes < a.writes {
		return -1
	}
	return int(float64((b.reads-a.reads)+(b.writes-a.writes)) / elapsed)
}

func netKBs(a, b sample) (in, out int) {
	elapsed := b.t.Sub(a.t).Seconds()
	if !a.netOK || !b.netOK || elapsed <= 0 || b.rxBytes < a.rxBytes || b.txBytes < a.txBytes {
		return -1, -1
	}
	in = int(float64(b.rxBytes-a.rxBytes) / 1024 / elapsed)
	out = int(float64(b.txBytes-a.txBytes) / 1024 / elapsed)
	return in, out
}


type tsLocalStatus struct {
	Self struct {
		KeyExpiry string `json:"KeyExpiry"`
	} `json:"Self"`
}

var tailscaleSocketPaths = []string{
	"/var/run/tailscale/tailscaled.sock",
	"/var/packages/Tailscale/var/tailscaled.sock",
}

func tailscaleSocketPath() string {
	for _, p := range tailscaleSocketPaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func tailscaleSocketExists() bool {
	return tailscaleSocketPath() != ""
}

// Returns (daysUntilExpiry, expiryEnabled, available).
// available=false means tailscale is not running on this host.
// expiryEnabled=false means key expiry is disabled (daysUntilExpiry is meaningless).
func tailscaleExpiryDays() (days int, expiryEnabled bool, available bool) {
	sockPath := tailscaleSocketPath()
	if sockPath == "" {
		return 0, false, false
	}

	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
	}

	resp, err := client.Get("http://local-tailscaled.sock/localapi/v0/status")
	if err != nil {
		log.Printf("Tailscale LocalAPI warning: %v", err)
		return 0, false, false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Tailscale LocalAPI warning: status %d", resp.StatusCode)
		return 0, false, false
	}

	var status tsLocalStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		log.Printf("Tailscale LocalAPI warning: decode error: %v", err)
		return 0, false, false
	}

	keyExpiry := status.Self.KeyExpiry
	if keyExpiry == "" || keyExpiry == "0001-01-01T00:00:00Z" {
		return 0, false, true
	}

	expiry, err := time.Parse(time.RFC3339, keyExpiry)
	if err != nil {
		log.Printf("Tailscale LocalAPI warning: parse KeyExpiry %q: %v", keyExpiry, err)
		return 0, false, true
	}

	days = int(time.Until(expiry).Hours() / 24)
	if days < 0 {
		days = 0
	}
	return days, true, true
}