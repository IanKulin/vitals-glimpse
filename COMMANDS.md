* Run `go run main.go`
* Build executable: `go build -o vitals-glimpse main.go`

* Copy executable (from host directory) `scp ian@192.168.100.41:vitals-glimpse/vitals-glimpse vitals-glimpse`
* Ansible file to install everywhere: `ansible-playbook vg-install.yml --ask-vault-pass`
