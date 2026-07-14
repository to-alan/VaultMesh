export type RepositoryEngine = 'restic-native' | 's3-preset' | 'rclone'
export type RepositoryFieldType = 'text' | 'password' | 'number' | 'select'

export interface RepositoryField {
  key: string
  label: string
  type: RepositoryFieldType
  required?: boolean
  placeholder?: string
  help?: string
  default?: string
  options?: { value: string; label: string }[]
  visibleWhen?: { key: string; values: string[] }[]
}

export interface RepositoryProviderDefinition {
  id: string
  label: string
  group: string
  engine: RepositoryEngine
  summary: string
  warning?: string
  fields: RepositoryField[]
}

const text = (key: string, label: string, required = true, placeholder = '', help = ''): RepositoryField => ({
  key, label, type: 'text', required, placeholder, help,
})
const password = (key: string, label: string, required = true, help = ''): RepositoryField => ({
  key, label, type: 'password', required, help,
})
const select = (key: string, label: string, options: { value: string; label: string }[], defaultValue: string, help = ''): RepositoryField => ({
  key, label, type: 'select', required: true, options, default: defaultValue, help,
})
const visible = (field: RepositoryField, key: string, ...values: string[]): RepositoryField => ({ ...field, visibleWhen: [{ key, values }] })
const visibleAll = (field: RepositoryField, ...conditions: { key: string; values: string[] }[]): RepositoryField => ({ ...field, visibleWhen: conditions })

const accessKeyFields: RepositoryField[] = [
  text('access_key', 'Access Key ID'),
  password('secret_key', 'Secret Access Key'),
  password('session_token', 'Session Token', false, '仅临时凭据需要；长期密钥留空。'),
]
const endpointS3Fields = (regionDefault = ''): RepositoryField[] => [
  text('endpoint', 'S3 Endpoint', true, 'https://s3.example.com', '填写厂商提供的 API Endpoint，不是控制台网址。'),
  text('bucket', 'Bucket', true, 'vaultmesh-backups'),
  text('region', 'Region', false, regionDefault || 'us-east-1'),
  ...accessKeyFields,
  select('bucket_lookup', 'Bucket 寻址方式', [
    { value: 'auto', label: 'Auto（推荐）' },
    { value: 'dns', label: 'Virtual-hosted / DNS' },
    { value: 'path', label: 'Path style' },
  ], 'auto'),
]

export const repositoryProviders: RepositoryProviderDefinition[] = [
  {
    id: 'local', label: '本地目录 / 挂载盘', group: '本地与服务器', engine: 'restic-native',
    summary: 'Agent 可访问的绝对路径，可指向本地磁盘、NFS 或 SMB 挂载点。',
    warning: '这是每台 Agent 的本地路径；若各服务器不共享同一挂载，请为它们建立不同渠道。',
    fields: [text('path', '绝对路径', true, '/mnt/backup/vaultmesh')],
  },
  {
    id: 'sftp', label: 'SFTP', group: '本地与服务器', engine: 'restic-native',
    summary: '使用 Restic 原生 SFTP 后端，通过 SSH 写入远程目录。',
    warning: 'VaultMesh 不保存 SSH 密码或私钥；请先在 Agent 上配置免交互公钥登录与 known_hosts。',
    fields: [text('host', '主机', true, 'backup.example.com'), { ...text('port', '端口', true, '22'), type: 'number', default: '22' }, text('username', '用户名', true, 'backup'), text('path', '远程绝对路径', true, '/srv/restic')],
  },
  {
    id: 'rest_server', label: 'Restic REST Server', group: '本地与服务器', engine: 'restic-native',
    summary: '连接官方 rest-server 或兼容的 Restic REST 协议服务。',
    fields: [text('endpoint', '服务地址', true, 'https://backup.example.com:8000'), text('username', 'HTTP 用户名', false), password('http_password', 'HTTP 密码', false)],
  },
  {
    id: 'amazon_s3', label: 'Amazon S3', group: 'S3 与兼容对象存储', engine: 's3-preset',
    summary: 'AWS S3 官方服务，Endpoint 根据 Region 自动生成。',
    fields: [text('bucket', 'Bucket', true, 'vaultmesh-backups'), text('region', 'AWS Region', true, 'us-east-1'), ...accessKeyFields],
  },
  {
    id: 'cloudflare_r2', label: 'Cloudflare R2', group: 'S3 与兼容对象存储', engine: 's3-preset',
    summary: '使用 R2 的 S3 兼容 API；Region 固定为 auto，使用 Path style。',
    fields: [text('account_id', 'Cloudflare Account ID', true, '32 位 Account ID'), select('jurisdiction', '数据管辖区', [{ value: 'default', label: 'Global / 默认' }, { value: 'eu', label: 'European Union' }, { value: 'fedramp', label: 'FedRAMP' }], 'default'), text('bucket', 'Bucket', true, 'vaultmesh-backups'), ...accessKeyFields.slice(0, 2)],
  },
  { id: 'minio', label: 'MinIO', group: 'S3 与兼容对象存储', engine: 's3-preset', summary: '自建或托管的 MinIO S3 兼容服务。', fields: endpointS3Fields('us-east-1') },
  { id: 'wasabi', label: 'Wasabi', group: 'S3 与兼容对象存储', engine: 's3-preset', summary: 'Wasabi S3 对象存储，按区域填写 Service URL。', fields: endpointS3Fields() },
  { id: 'alibaba_oss', label: '阿里云 OSS（S3 API）', group: 'S3 与兼容对象存储', engine: 's3-preset', summary: '通过 OSS 的 S3 兼容 API 接入，强制使用 DNS 寻址。', fields: endpointS3Fields().filter((field) => field.key !== 'bucket_lookup') },
  { id: 'tencent_cos', label: '腾讯云 COS（S3 API）', group: 'S3 与兼容对象存储', engine: 's3-preset', summary: '通过腾讯云 COS 的 S3 兼容 Endpoint 接入。', fields: endpointS3Fields() },
  { id: 'huawei_obs', label: '华为云 OBS（S3 API）', group: 'S3 与兼容对象存储', engine: 's3-preset', summary: '通过华为云 OBS 的 S3 兼容 Endpoint 接入。', fields: endpointS3Fields() },
  { id: 'qiniu_kodo', label: '七牛云 Kodo（S3 API）', group: 'S3 与兼容对象存储', engine: 's3-preset', summary: '通过七牛 Kodo 的 S3 兼容 Endpoint 接入。', fields: endpointS3Fields() },
  { id: 'backblaze_b2_s3', label: 'Backblaze B2（S3 API）', group: 'S3 与兼容对象存储', engine: 's3-preset', summary: 'Restic 官方更推荐的 B2 接入方式，使用 B2 S3 兼容 API。', fields: endpointS3Fields() },
  { id: 's3_compatible', label: '其他 S3 兼容存储', group: 'S3 与兼容对象存储', engine: 's3-preset', summary: '适用于提供标准 S3 Endpoint、Bucket 和访问密钥的其他厂商。', fields: endpointS3Fields() },
  {
    id: 'openstack_swift', label: 'OpenStack Swift', group: '其他 Restic 原生后端', engine: 'restic-native',
    summary: 'Swift v1、Keystone v3 密码、Application Credential 或现成 Token。',
    fields: [
      text('container', 'Container', true, 'vaultmesh-backups'),
      select('auth_mode', '认证方式', [{ value: 'v3_password', label: 'Keystone v3 用户密码' }, { value: 'application_credential', label: 'Application Credential' }, { value: 'token', label: 'Storage URL + Token' }, { value: 'v1', label: 'Swift v1' }], 'v3_password'),
      visible(text('auth_url', 'Auth URL', true, 'https://keystone.example.com/v3'), 'auth_mode', 'v3_password', 'application_credential'),
      visible(text('region', 'Region', false), 'auth_mode', 'v3_password', 'application_credential'),
      visible(text('username', '用户名'), 'auth_mode', 'v3_password'), visible(password('swift_password', '密码'), 'auth_mode', 'v3_password'),
      visible(text('project_name', 'Project Name'), 'auth_mode', 'v3_password'), visible({ ...text('user_domain', 'User Domain', true, 'Default'), default: 'Default' }, 'auth_mode', 'v3_password'), visible({ ...text('project_domain', 'Project Domain', true, 'Default'), default: 'Default' }, 'auth_mode', 'v3_password'),
      visible(select('credential_kind', 'Credential 标识方式', [{ value: 'id', label: 'Credential ID' }, { value: 'name', label: 'Credential Name' }], 'id'), 'auth_mode', 'application_credential'),
      visibleAll(text('application_id', 'Application Credential ID'), { key: 'auth_mode', values: ['application_credential'] }, { key: 'credential_kind', values: ['id'] }), visibleAll(text('application_name', 'Application Credential Name'), { key: 'auth_mode', values: ['application_credential'] }, { key: 'credential_kind', values: ['name'] }), visibleAll(text('application_username', '用户名（Name 模式）'), { key: 'auth_mode', values: ['application_credential'] }, { key: 'credential_kind', values: ['name'] }), visibleAll({ ...text('application_user_domain', 'User Domain（Name 模式）'), default: 'Default' }, { key: 'auth_mode', values: ['application_credential'] }, { key: 'credential_kind', values: ['name'] }), visible(password('application_secret', 'Application Credential Secret'), 'auth_mode', 'application_credential'),
      visible(text('storage_url', 'Storage URL'), 'auth_mode', 'token'), visible(password('auth_token', 'Auth Token'), 'auth_mode', 'token'),
      visible(text('st_auth', 'ST_AUTH'), 'auth_mode', 'v1'), visible(text('st_user', 'ST_USER'), 'auth_mode', 'v1'), visible(password('st_key', 'ST_KEY'), 'auth_mode', 'v1'),
      text('container_policy', '新建 Container Policy', false, '', '仅当 Restic 需要创建 Container 时使用。'),
    ],
  },
  {
    id: 'backblaze_b2', label: 'Backblaze B2（原生）', group: '其他 Restic 原生后端', engine: 'restic-native',
    summary: 'Restic 原生 B2 后端。', warning: 'Restic 官方建议新配置优先使用上面的 B2 S3 API 预设。',
    fields: [text('bucket', 'Bucket'), text('account_id', 'Application Key ID'), password('account_key', 'Application Key')],
  },
  {
    id: 'azure_blob', label: 'Azure Blob Storage', group: '其他 Restic 原生后端', engine: 'restic-native',
    summary: '支持 Storage Account Key、SAS、Managed Identity、Workload Identity 或 Agent 上的 Azure CLI。',
    fields: [text('container', 'Container'), text('account_name', 'Storage Account Name'), select('auth_mode', '认证方式', [{ value: 'account_key', label: 'Account Key' }, { value: 'sas', label: 'SAS Token' }, { value: 'ambient', label: 'Managed / Workload Identity' }, { value: 'azure_cli', label: 'Agent 上的 Azure CLI 登录' }], 'account_key'), visible(password('account_key', 'Account Key'), 'auth_mode', 'account_key'), visible(password('sas_token', 'SAS Token'), 'auth_mode', 'sas'), text('endpoint_suffix', 'Endpoint Suffix', false, 'core.windows.net'), select('access_tier', 'Access Tier', [{ value: 'Hot', label: 'Hot' }, { value: 'Cool', label: 'Cool' }, { value: 'Cold', label: 'Cold' }], 'Hot')],
  },
  {
    id: 'google_cloud_storage', label: 'Google Cloud Storage', group: '其他 Restic 原生后端', engine: 'restic-native',
    summary: 'GCS 原生后端；服务账号 JSON 文件路径必须在执行备份的 Agent 上可读。',
    fields: [text('bucket', 'Bucket'), text('project_id', 'Google Project ID'), select('auth_mode', '认证方式', [{ value: 'service_account', label: 'Agent 上的 Service Account JSON' }, { value: 'access_token', label: 'Access Token' }, { value: 'ambient', label: 'Agent 默认凭据 / Workload Identity' }], 'service_account'), visible(text('credentials_path', 'Agent 凭据文件路径', true, '/etc/vaultmesh/gcp.json'), 'auth_mode', 'service_account'), visible(password('access_token', 'Access Token'), 'auth_mode', 'access_token')],
  },
  {
    id: 'rclone', label: 'rclone 远端', group: 'rclone 扩展', engine: 'rclone', summary: '连接 Agent 上已配置的任意 rclone remote。',
    warning: '每台会使用此渠道的 Agent 都必须安装 rclone，并配置同名 remote。', fields: [text('remote', 'rclone Remote 名称', true, 'archive'), text('remote_path', '远端路径', false, 'vaultmesh')],
  },
  { id: 'webdav_rclone', label: 'WebDAV（经 rclone）', group: 'rclone 扩展', engine: 'rclone', summary: '适用于 Nextcloud、坚果云等 WebDAV 服务。', warning: '先在 Agent 上用 rclone config 创建 WebDAV remote。', fields: [text('remote', 'WebDAV Remote 名称', true, 'webdav'), text('remote_path', '远端路径', false, 'vaultmesh')] },
  { id: 'onedrive_rclone', label: 'Microsoft OneDrive（经 rclone）', group: 'rclone 扩展', engine: 'rclone', summary: '通过 rclone 已授权的 OneDrive remote。', warning: 'OAuth 授权由 rclone 管理，VaultMesh 不接收 Microsoft 授权码。', fields: [text('remote', 'OneDrive Remote 名称', true, 'onedrive'), text('remote_path', '远端路径', false, 'vaultmesh')] },
  { id: 'google_drive_rclone', label: 'Google Drive（经 rclone）', group: 'rclone 扩展', engine: 'rclone', summary: '通过 rclone 已授权的 Google Drive remote；与 GCS 不同。', warning: 'OAuth 授权由 rclone 管理，VaultMesh 不接收 Google 授权码。', fields: [text('remote', 'Google Drive Remote 名称', true, 'gdrive'), text('remote_path', '远端路径', false, 'vaultmesh')] },
  { id: 'dropbox_rclone', label: 'Dropbox（经 rclone）', group: 'rclone 扩展', engine: 'rclone', summary: '通过 rclone 已授权的 Dropbox remote。', warning: 'OAuth 授权由 rclone 管理，VaultMesh 不接收 Dropbox 授权码。', fields: [text('remote', 'Dropbox Remote 名称', true, 'dropbox'), text('remote_path', '远端路径', false, 'vaultmesh')] },
]

export const repositoryProviderGroups = Array.from(new Set(repositoryProviders.map((provider) => provider.group)))

export function repositoryProvider(id: string): RepositoryProviderDefinition {
  return repositoryProviders.find((provider) => provider.id === id) ?? repositoryProviders[0]
}

export function repositoryFieldVisible(field: RepositoryField, values: Record<string, string>): boolean {
  return !field.visibleWhen || field.visibleWhen.every((condition) => condition.values.includes(values[condition.key] ?? ''))
}

export function repositoryDefaults(providerID: string): Record<string, string> {
  return Object.fromEntries(repositoryProvider(providerID).fields.map((field) => [field.key, field.default ?? '']))
}

function cleanPath(value: string): string {
  return value.trim().replace(/^\/+|\/+$/g, '')
}

export function buildRepositoryURL(providerID: string, values: Record<string, string>, prefixValue: string): string {
  const prefix = cleanPath(prefixValue)
  const suffix = (base: string) => prefix ? `${base}/${prefix}` : base
  if (providerID === 'local') return values.path?.trim() || ''
  if (providerID === 'sftp') {
    const path = cleanPath(values.path || '')
    return values.host && values.username && path ? `sftp://${encodeURIComponent(values.username)}@${values.host.trim()}:${values.port || '22'}//${path}` : ''
  }
  if (providerID === 'rest_server') {
    const endpoint = (values.endpoint || '').trim().replace(/\/+$/, '')
    return endpoint ? suffix(`rest:${endpoint}`) : ''
  }
  if (providerID === 'openstack_swift') return values.container ? `swift:${values.container}:/${prefix}` : ''
  if (providerID === 'backblaze_b2') return values.bucket ? `b2:${values.bucket}:${prefix}` : ''
  if (providerID === 'azure_blob') return values.container ? `azure:${values.container}:/${prefix}` : ''
  if (providerID === 'google_cloud_storage') return values.bucket ? `gs:${values.bucket}:/${prefix}` : ''
  if (repositoryProvider(providerID).engine === 'rclone') {
    const path = cleanPath(values.remote_path || prefix)
    return values.remote ? `rclone:${values.remote}:${path}` : ''
  }
  let endpoint = (values.endpoint || '').trim().replace(/\/+$/, '')
  if (providerID === 'amazon_s3' && values.region) endpoint = `https://s3.${values.region}.amazonaws.com`
  if (providerID === 'cloudflare_r2' && values.account_id) {
    const jurisdiction = values.jurisdiction && values.jurisdiction !== 'default' ? `.${values.jurisdiction}` : ''
    endpoint = `https://${values.account_id}${jurisdiction}.r2.cloudflarestorage.com`
  }
  if (!endpoint || !values.bucket) return ''
  return suffix(`s3:${endpoint}/${values.bucket}`)
}

export function buildRepositoryEnvironment(providerID: string, values: Record<string, string>): Record<string, string> {
  const environment: Record<string, string> = {}
  const put = (key: string, value?: string) => { if (value?.trim()) environment[key] = value.trim() }
  if (repositoryProvider(providerID).engine === 's3-preset') {
    put('AWS_ACCESS_KEY_ID', values.access_key); put('AWS_SECRET_ACCESS_KEY', values.secret_key); put('AWS_SESSION_TOKEN', values.session_token)
    put('AWS_DEFAULT_REGION', providerID === 'cloudflare_r2' ? 'auto' : values.region)
  } else if (providerID === 'rest_server') {
    put('RESTIC_REST_USERNAME', values.username); put('RESTIC_REST_PASSWORD', values.http_password)
  } else if (providerID === 'backblaze_b2') {
    put('B2_ACCOUNT_ID', values.account_id); put('B2_ACCOUNT_KEY', values.account_key)
  } else if (providerID === 'azure_blob') {
    put('AZURE_ACCOUNT_NAME', values.account_name); put('AZURE_ACCOUNT_KEY', values.account_key); put('AZURE_ACCOUNT_SAS', values.sas_token); put('AZURE_ENDPOINT_SUFFIX', values.endpoint_suffix)
    if (values.auth_mode === 'azure_cli') environment.AZURE_FORCE_CLI_CREDENTIAL = 'true'
  } else if (providerID === 'google_cloud_storage') {
    put('GOOGLE_PROJECT_ID', values.project_id); put('GOOGLE_APPLICATION_CREDENTIALS', values.credentials_path); put('GOOGLE_ACCESS_TOKEN', values.access_token)
  } else if (providerID === 'openstack_swift') {
    if (values.auth_mode === 'v1') { put('ST_AUTH', values.st_auth); put('ST_USER', values.st_user); put('ST_KEY', values.st_key) }
    else if (values.auth_mode === 'token') { put('OS_STORAGE_URL', values.storage_url); put('OS_AUTH_TOKEN', values.auth_token) }
    else {
      put('OS_AUTH_URL', values.auth_url); put('OS_REGION_NAME', values.region)
      if (values.auth_mode === 'application_credential') { put('OS_APPLICATION_CREDENTIAL_ID', values.application_id); put('OS_APPLICATION_CREDENTIAL_NAME', values.application_name); put('OS_USERNAME', values.application_username); put('OS_USER_DOMAIN_NAME', values.application_user_domain); put('OS_APPLICATION_CREDENTIAL_SECRET', values.application_secret) }
      else { put('OS_USERNAME', values.username); put('OS_PASSWORD', values.swift_password); put('OS_PROJECT_NAME', values.project_name); put('OS_USER_DOMAIN_NAME', values.user_domain); put('OS_PROJECT_DOMAIN_NAME', values.project_domain) }
    }
    put('SWIFT_DEFAULT_CONTAINER_POLICY', values.container_policy)
  }
  return environment
}

export function buildRepositoryOptions(providerID: string, values: Record<string, string>): Record<string, string> {
  const options: Record<string, string> = {}
  if (repositoryProvider(providerID).engine === 's3-preset') options['s3.bucket-lookup'] = providerID === 'cloudflare_r2' ? 'path' : providerID === 'alibaba_oss' ? 'dns' : values.bucket_lookup || 'auto'
  if (providerID === 'azure_blob' && values.access_tier) options['azure.access-tier'] = values.access_tier
  return options
}

export function missingRepositoryFields(providerID: string, values: Record<string, string>): string[] {
  return repositoryProvider(providerID).fields
    .filter((field) => field.required && repositoryFieldVisible(field, values) && !(values[field.key] ?? '').trim())
    .map((field) => field.label)
}

export function engineLabel(engine: RepositoryEngine): string {
  return { 'restic-native': 'Restic 原生', 's3-preset': 'S3 兼容预设', rclone: 'rclone 扩展' }[engine]
}
