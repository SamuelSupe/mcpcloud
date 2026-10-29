# 程序包发布流程

在仓库根目录执行，需 Go 1.26 工具链和 Git。输出默认位于被 Git 忽略的 `dist/`。正式构建要求源码已提交且工作树干净。

```sh
go test -count=1 ./...
go run ./scripts/release --version v0.2.0 --out dist
```

构建工具只打包固定白名单：程序、安装 / 初始化 / Agent 操作 / 配置说明、配置示例、权限 / DSL / 平台说明、更新说明、实际链接依赖的版本列表及其许可 / NOTICE 文件。不复制部署目录、凭证、私有资源记录或当前主机环境文件。Linux 使用静态 Go 构建；四平台内嵌同一源提交和提交 UTC 时间。

输出包含四个压缩包、独立安装说明、`release-manifest.json` 与 `SHA256SUMS`。`dist/work/` 内的单程序是构建中间产物，不作为 Release 附件。

v0.2.0 的 smoke 检查还会执行离线 `init`、`configure` 预览与保存、`doctor --json`、`client-config`，并按生成的参数启动 MCP 完成握手；还会在隔离文件中验证 Codex 原生 TOML 合并、重复安装保留已有设置，并从该注册启动 MCP。复验 v0.1.4 等历史发布包时，应使用对应 tag 的验证工具；下面的版本号示例也应随待发布版本调整。

解压后验证实际包内的程序，而非工作树旧程序：

```sh
go run ./scripts/smoke --binary /absolute/path/extracted/mcpcloud --version 0.2.0 --commit FULL_RELEASE_COMMIT --os linux --arch amd64 --out verification-linux-amd64.json
```

Windows 指定 exe、`--os windows --arch amd64` 或 `--arch arm64`，须在对应架构主机运行。Linux ARM64 若使用仿真，在 Linux amd64 主机添加 `--runner /absolute/path/qemu-aarch64-static --os linux --arch arm64`；结果必须标明仿真。检查不需要云账号，不执行云读取或修改。

将 smoke 验证结果作为 Release 附件后，重新生成校验清单以覆盖所有分发资产；校验清单自身不纳入自身哈希。发布前按 manifest 核对版本、源提交、目标架构、各包哈希以及文档。更新说明保留当前限制，不能把启动验证写成云端全量复验。

将发布准备合并主线后，从远端检出准确提交再构建、验证，创建对应 Git tag，并发布 GitHub Release。Tag、二进制内嵌 commit、Release target 和 manifest 必须一致。不要移动已有发布 tag 或覆盖同名资产；修订使用新版本。没有附带 Docker 镜像。

正式虚拟机验收使用 .github/workflows/release-vm-validation.yml：一个构建任务生成四种压缩包，四个匹配 OS / 架构的独立 GitHub 标准运行器验证同一批解压包。ARM64 运行器验证 Go 主机架构，并非 QEMU。工作流不接入云凭证、不部署、不自动创建 Release；仅下载全部成功结果后人工执行发布流程。v0.1.0 的 WSL / 仿真证据保留，不能追溯改记为本轮 VM 证据。
