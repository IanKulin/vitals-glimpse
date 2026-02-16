# vitals-glimpse

`vitals-glimpse` is a simple endpoint that exposes a Linux host or container's memory, disk, and cpu usage as percentages. 

The JSON returned also has status keywords for the memory, disk, and cpu which makes it a good candidate for simple monitoring tools such as [Uptime Kuma](https://github.com/louislam/uptime-kuma) which can provide a binary up/down response based on the presence or absence of a keyword. The thresholds for these have defaults that can be overwritten with command line flags.

To access the endpoint in a browser, visit `<server name>:10321/vitals` 

## Output

A sample response might be:
```
{
	"title": "vitals-glimpse",
	"version": 0.1,
	"mem_status": "mem_okay",
	"mem_percent": 37,
	"disk_status": "disk_okay",
	"disk_percent": 15,
	"cpu_status":"cpu_okay",
	"cpu_percent":2
}
```
or if the thresholds are exceeded:
```
{
	"title": "vitals-glimpse",
	"version": 0.1,
	"mem_status": "mem_fail",
	"mem_percent": 91,
	"disk_status": "disk_fail",
	"disk_percent": 81,
	"cpu_status":"cpu_fail",
	"cpu_percent":92
}
```
The disk usage is based on the `/` root mount point

The default thresholds for the status keywords are:
* `mem_okay` - below 90% memory usage
* `disk_okay` - below 80% disk usage
* `cpu_okay` - below 90% cpu usage

## Command-Line Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-mem` | 90 | Memory usage threshold (percent) |
| `-disk` | 80 | Disk usage threshold (percent) |
| `-cpu` | 90 | CPU usage threshold (percent) |
| `-port` | 10321 | Server port |

Example:
```
./vitals-glimpse -mem 85 -disk 70 -cpu 95 -port 8080
```


## Building on MacBook
- `GOARCH=amd64 GOOS=linux go build`
- `GOARCH=amd64 GOOS=linux go build -ldflags="-s -w"` for distribution

The `-s -w` flags strip debug info and symbol tables, reducing binary size by ~20-30%.

## Testing on Debian LXC
- `scp vitals-glimpse ian@ct390-test:vitals-glimpse`
- `ssh ian@ct390-test 'chmod +x vitals-glimpse'`
- `ssh ian@ct390-test 'nohup ./vitals-glimpse > output.log 2>&1 & echo $! > vitals-glimpse.pid'`
- 'http://ct390-test:10321/vitals"
- `ssh ian@ct390-test 'kill $(cat vitals-glimpse.pid)'`


## Versions
- 0.2 MVP
- 0.3 Container detection for CPU, flags for thresholds
- 0.4 Security