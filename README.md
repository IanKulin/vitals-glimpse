# vitals-glimpse

`vitals-glimpse` is a very simple REST API that exposes a Linux host's memory and disk usage as percentages. 

The JSON returned also has status keywords for the memory and disk which makes it a good candidate for simple monitoring tools such as [Uptime Kuma](https://github.com/louislam/uptime-kuma) which can provide a binary up/down response based on the presence or absence of a keyword.

To access the endpoint in a browser, visit `<server name>:10321/vitals` 

A sample response might be:
```
{
	"title": "vitals-glimpse",
	"version": 0.1,
	"mem_status": "mem_okay",
	"mem_percent": 37,
	"disk_status": "disk_okay",
	"disk_percent": 15
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
	"disk_percent": 81
}
```

The threholds for the status keywords are:
* `mem_okay` - below 90% memory usage
* `disk_okay` - below 80% disk usage

The disk usage is based on the `/` root mount point