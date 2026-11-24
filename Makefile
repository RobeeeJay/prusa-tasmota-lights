MODULE  := github.com/robeeejay/prusa-tasmota-lights
NAME    := tasmota-lights
VERSION := $(shell sed -n 's/^version: //p' module.yaml)

.PHONY: test release copy

test:
	go vet ./...
	go test -race ./...

# A release is the directory `prusa-connect module add -from` reads: the
# manifest, one binary per platform and the checksums of both.
release: test
	rm -rf release && mkdir release
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags=-s -o release/$(NAME)_linux_arm64 .
	cp module.yaml release/
	cd release && shasum -a 256 module.yaml $(NAME)_linux_arm64 > checksums.txt

# Puts the release and the install script on the Pi. The install itself needs
# root, for docker, so it is run there by hand.
copy: release
	@test -n "$(PI)" || { echo "Name the server: make copy PI=user@host"; exit 1; }
	ssh $(PI) 'rm -rf ~/$(NAME)-$(VERSION) && mkdir -p ~/$(NAME)-$(VERSION)'
	scp -q release/* scripts/install.sh $(PI):~/$(NAME)-$(VERSION)/
	@echo "On the Pi: sudo bash ~/$(NAME)-$(VERSION)/install.sh"
