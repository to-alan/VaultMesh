# VaultMesh 存储仓库支持矩阵

VaultMesh 不定义一套脱离执行引擎的“万能云存储表单”。项目实际使用 Restic 执行备份，所以协议、Repository URL、环境变量和后端选项以 [Restic 官方仓库文档](https://restic.readthedocs.io/en/stable/030_preparing_a_new_repo.html) 为规范；[1Panel 备份账号表单源码](https://github.com/1Panel-dev/1Panel/blob/dev-v2/frontend/src/views/setting/backup-account/operate/index.vue) 用于核对面向用户的字段分组；[Kopia 仓库文档](https://kopia.io/docs/repositories/) 用于交叉验证主流备份产品的存储分类。

## 统一模型

所有渠道都包含：

- 渠道名称；
- Restic 仓库密码，用于端到端加密，不等于云厂商 Secret Key；
- 仓库根路径或目录前缀；
- 该协议所需的连接字段和认证字段。

渠道在控制面中全局存在，不直接绑定服务器。分配给项目后，控制面会在基础 URL 后追加 `/<server-id>`，避免不同服务器并发写入同一个 Restic 仓库。密码、环境凭据和允许的 Restic 后端选项统一进入 AES-256-GCM 加密载荷；仓库列表和 API 响应不会回传这些值。

## 已引入类型

| 分层 | UI 类型 / provider | 必填连接字段 | 认证字段 | 实际后端 |
|---|---|---|---|---|
| Restic 原生 | 本地目录 `local` | Agent 上的绝对路径 | 无 | Local |
| Restic 原生 | SFTP `sftp` | Host、Port、User、远程绝对路径 | Agent 上预配置 SSH 公钥与 `known_hosts` | SFTP |
| Restic 原生 | REST Server `rest_server` | HTTP(S) Endpoint | 可选 Basic Auth 用户名、密码 | REST |
| S3 预设 | Amazon S3 `amazon_s3` | Region、Bucket | Access Key、Secret Key；可选 Session Token | S3 |
| S3 预设 | Cloudflare R2 `cloudflare_r2` | Account ID、Jurisdiction、Bucket | Access Key、Secret Key | S3，Region=`auto`，Path style |
| S3 预设 | MinIO `minio` | Endpoint、Bucket、Region | Access Key、Secret Key | S3 |
| S3 预设 | Wasabi `wasabi` | Endpoint、Bucket、Region | Access Key、Secret Key | S3 |
| S3 预设 | 阿里云 OSS `alibaba_oss` | S3 Endpoint、Bucket、Region | Access Key、Secret Key | S3，DNS style |
| S3 预设 | 腾讯云 COS `tencent_cos` | S3 Endpoint、Bucket、Region、寻址方式 | Access Key、Secret Key | S3 |
| S3 预设 | 华为云 OBS `huawei_obs` | S3 Endpoint、Bucket、Region、寻址方式 | Access Key、Secret Key | S3 |
| S3 预设 | 七牛云 Kodo `qiniu_kodo` | S3 Endpoint、Bucket、Region、寻址方式 | Access Key、Secret Key | S3 |
| S3 预设 | Backblaze B2 S3 `backblaze_b2_s3` | S3 Endpoint、Bucket、Region、寻址方式 | Application Key ID、Application Key 按 S3 字段填写 | S3 |
| S3 预设 | 其他 S3 `s3_compatible` | Endpoint、Bucket、Region、寻址方式 | Access Key、Secret Key；可选 Session Token | S3 |
| Restic 原生 | OpenStack Swift `openstack_swift` | Container、认证模式对应字段 | Swift v1、Keystone v3 Password、Application Credential 或 Storage Token | Swift |
| Restic 原生 | Backblaze B2 `backblaze_b2` | Bucket | Account/Application Key ID、Key | B2；新配置优先使用 B2 S3 |
| Restic 原生 | Azure Blob `azure_blob` | Container、Storage Account Name | Account Key、SAS Token、Managed/Workload Identity 或 Agent 的 Azure CLI；可选 Endpoint Suffix | Azure |
| Restic 原生 | Google Cloud Storage `google_cloud_storage` | Bucket、Project ID | Agent 上的 Service Account JSON、Access Token 或环境默认凭据 | GCS，不是 Google Drive |
| rclone | 通用 `rclone` | Agent 上已存在的 Remote 名称、路径 | 由 rclone 配置管理 | rclone |
| rclone | WebDAV `webdav_rclone` | WebDAV Remote 名称、路径 | 由 rclone 配置管理 | rclone |
| rclone | OneDrive `onedrive_rclone` | OneDrive Remote 名称、路径 | OAuth 由 rclone 管理 | rclone |
| rclone | Google Drive `google_drive_rclone` | Google Drive Remote 名称、路径 | OAuth 由 rclone 管理 | rclone |
| rclone | Dropbox `dropbox_rclone` | Dropbox Remote 名称、路径 | OAuth 由 rclone 管理 | rclone |

S3 不是某一家厂商，而是一套被大量对象存储实现的兼容 API。厂商预设共享 Restic S3 后端，但保留各自 Endpoint、Region 和 Bucket 寻址差异。没有列出的 S3 厂商应使用“其他 S3 兼容存储”，不需要为每个品牌复制一套后端代码。

## 为什么没有直接复制 1Panel 的所有类型

1Panel 会在自身进程中直接对接 OSS、COS、Kodo、WebDAV 和网盘 SDK；VaultMesh 则在 Agent 上执行 Restic。两者的执行边界不同，所以不能把 1Panel 的类型名直接当成 Restic 能力。VaultMesh 只采用其成熟的字段布局：

- S3 系对象存储统一为 Endpoint、Bucket、Region、Access Key、Secret Key、寻址方式；
- SFTP 统一为地址、端口、用户、认证、目录；VaultMesh 当前出于无人值守和密钥边界考虑，只接收 Agent 已配置的 SSH 公钥认证；
- WebDAV 和 OAuth 网盘明确走 rclone，不在 VaultMesh 控制面保存第三方 OAuth 授权码。

[Duplicati 的开源项目](https://github.com/duplicati/duplicati) 也展示了更广的厂商插件列表，但其插件体系与 Restic 后端并不等价，因此它只作为覆盖面参考，不作为 VaultMesh 的协议规范。

## Agent 前置条件

- 所有类型都需要 Restic。
- rclone 分层的类型还需要 rclone，并要求每台会执行该渠道的 Agent 存在同名 remote。官方 Docker Agent 镜像已经包含 rclone。
- SFTP 需要 Agent 的运行用户能够免交互登录，并已校验远端主机密钥。
- GCS 的 Service Account 路径是 Agent 本地路径；多个 Agent 共用渠道时，必须在每台机器上部署到同一路径。
- 本地目录可以是磁盘路径或已经由操作系统挂载的 NFS/SMB 目录；VaultMesh 不负责挂载网络文件系统。

## 安全边界

后端只接受精确的 provider、环境变量与 Restic option 白名单。不会接受任意环境变量、`sftp.command`、`rclone.program` 等可执行命令型参数，避免仓库配置变成 Agent 上的命令执行入口。S3/REST URL 禁止 userinfo、查询参数和 fragment；SFTP URL 只允许用户名，禁止内嵌密码。所有凭据必须进入独立的加密字段，避免仓库列表 API 回传 URL 时泄漏 Secret。当前“校验并预览”只验证字段与 URL 结构；真实连通性由对应 Agent 在首次 `restic snapshots` / `restic init` 时验证。
