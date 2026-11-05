# 赛博威胁地图 —— 构建入口

.PHONY: build build-arm64 build-linux run test vet clean

# 本机构建（macOS / Linux 通用）
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o cybermap ./cmd/cybermap

# Linux arm64 静态编译（树莓派 / 云主机）
build-arm64:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath \
		-ldflags="-s -w -extldflags '-static'" -o cybermap-arm64 ./cmd/cybermap

# Linux amd64 交叉编译
build-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
		-ldflags="-s -w -extldflags '-static'" -o cybermap-linux ./cmd/cybermap

# 本地开发运行（mock 模式，8080 端口）
run:
	go run ./cmd/cybermap serve --mock

# 全量测试（Go 竞态检测 + 前端逻辑测试）
test:
	go test -race ./...
	node web/test/run.mjs

# 前端逻辑测试（投影 / 点阵）
test-frontend:
	node web/test/run.mjs

# 静态检查
vet:
	go vet ./...

clean:
	rm -f cybermap cybermap-arm64 cybermap-linux
