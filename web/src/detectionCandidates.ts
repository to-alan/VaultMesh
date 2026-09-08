import type { DetectionReport, Project, Source } from './types'

export type CandidateKind = 'apps' | 'databases' | 'containers'
export type CandidateGroup = 'recommended' | 'review' | 'unavailable' | 'excluded'
export type DetectionSelection = Record<CandidateKind, number[]>
export interface DetectionCandidate {
  key: string
  kind: CandidateKind
  index: number
  title: string
  subtitle: string
  typeLabel: string
  group: CandidateGroup
  reason: string
  selectable: boolean
}

function imageName(image: string): string {
  return image.split('@')[0]!.split('/').at(-1)!.split(':')[0]!.toLowerCase()
}

function isPlatformImage(image: string): boolean {
  return /^ghcr\.io\/to-alan\/vaultmesh\/vaultmesh-(control|web|agent)(:|@|$)/.test(image)
}

function isDatabaseImage(image: string): boolean {
  return /^(mysql|mysql-server|mariadb|postgres|postgresql|timescaledb|percona|percona-server)$/.test(imageName(image))
}

function cleanPath(path: string): string {
  return path.replace(/\/+$/, '') || '/'
}

function within(path: string, parent: string): boolean {
  return cleanPath(path) === cleanPath(parent) || path.startsWith(cleanPath(parent) + '/')
}

function knownProjects(projects: Project[], serverID: string, predicate: (source: Source) => boolean): string {
  return projects.filter((project) => !project.archived_at && project.server_id === serverID && project.sources.some(predicate))
    .map((project) => `${project.name}${project.enabled ? '' : '（已暂停）'}`).join('、')
}

// The original array index is retained: sorting/filtering the UI must never
// select a different source. "Recommended" is a drafting suggestion, not a
// guarantee of consistent data or a successful backup.
export function detectionCandidates(report: DetectionReport, projects: Project[], serverID: string): DetectionCandidate[] {
  const candidates: DetectionCandidate[] = []
  const platformContainers = new Set((report.containers ?? []).filter((container) => container.exclusion_reason || isPlatformImage(container.image)).map((container) => container.name))
  const add = (kind: CandidateKind, index: number, title: string, subtitle: string, typeLabel: string, group: CandidateGroup, reason: string) => {
    candidates.push({ key: `${kind}:${index}`, kind, index, title, subtitle, typeLabel, group, reason, selectable: group === 'recommended' || group === 'review' })
  }

  for (const [index, db] of (report.databases ?? []).entries()) {
    const title = `${db.kind === 'mysql' ? 'MySQL' : 'PostgreSQL'} · ${db.container || '本机实例'}`
    const endpoint = db.port > 0 ? `${db.host}:${db.port}` : '未发布主机端口'
    let group: CandidateGroup = 'recommended'
    let reason = '使用逻辑导出；生成草稿后填写库名、账号和密码，端口可达不代表授权成功。'
    if (db.exclusion_reason || (db.container && platformContainers.has(db.container))) {
      group = 'excluded'; reason = db.exclusion_reason || 'VaultMesh 自身数据库，请使用专门的平台灾备方案。'
    } else if (!db.reachable || db.port <= 0) {
      group = 'unavailable'; reason = db.port <= 0 ? '未发布主机端口；请先配置 Agent 可访问的数据库端点。' : '端口不可达；检查数据库状态、监听地址和防火墙后重新探测。'
    } else if (!db.dump_tool) {
      group = 'unavailable'; reason = `Agent 缺少可用的 ${db.kind === 'mysql' ? 'mysqldump' : 'pg_dump'}；安装后重新探测。`
    } else {
      const names = knownProjects(projects, serverID, (source) => source.type === db.kind && source.database?.host === db.host && source.database?.port === db.port)
      if (names) { group = 'review'; reason = `已有项目使用此实例：${names}。可能是不同数据库，请核对库名，避免重复配置。` }
    }
    add('databases', index, title, endpoint, '数据库', group, reason)
  }

  for (const [index, app] of (report.apps ?? []).entries()) {
    let group: CandidateGroup = 'recommended'
    let reason = '发现应用标记；请确认业务数据路径，并排除缓存、依赖和临时文件。'
    const names = knownProjects(projects, serverID, (source) => source.type === 'files' && Boolean(source.paths?.some((path) => within(app.path, path))))
    const parent = report.apps?.find((other) => !other.exclusion_reason && other.path !== app.path && within(app.path, other.path))
    const excludedParent = report.apps?.find((other) => other.exclusion_reason && within(app.path, other.path))
    if (app.exclusion_reason || excludedParent?.exclusion_reason) { group = 'excluded'; reason = app.exclusion_reason || excludedParent!.exclusion_reason! }
    else if (!app.path.startsWith('/') || cleanPath(app.path) === '/' || /^\/(proc|sys|dev)(\/|$)/.test(app.path)) {
      group = 'excluded'; reason = '不是安全的业务目录，请手动指定需要保护的绝对路径。'
    } else if (names) { group = 'review'; reason = `已有项目包含此路径：${names}。是否被排除、是否正常备份仍需核对。` }
    else if (parent) { group = 'review'; reason = `位于候选目录 ${parent.path} 内，通常无需重复添加。` }
    else if (app.kind === 'compose') { group = 'review'; reason = 'Compose 配置不等于业务数据；请核对数据卷位置，避免直接复制运行中的数据库文件。' }
    add('apps', index, app.path.split('/').filter(Boolean).at(-1) || app.path, app.path, app.name, group, reason)
  }

  for (const [index, container] of (report.containers ?? []).entries()) {
    let group: CandidateGroup = 'recommended'
    let reason = '发现持久化挂载；请核对数据卷，写入中的数据需要应用一致性措施。'
    const names = knownProjects(projects, serverID, (source) => source.type === 'docker' && Boolean(source.docker?.containers.includes(container.name)))
    if (container.exclusion_reason || isPlatformImage(container.image)) {
      group = 'excluded'; reason = container.exclusion_reason || 'VaultMesh 自身组件，请使用专门的平台灾备方案。'
    } else if (isDatabaseImage(container.image) || report.databases?.some((db) => db.container === container.name)) {
      group = 'unavailable'; reason = '数据库容器请使用上方的数据库逻辑导出；直接复制数据卷可能无法恢复。'
    } else if (!container.mounts?.length) {
      group = 'unavailable'; reason = '未发现持久化挂载，或挂载信息读取失败；请核实后手动配置。'
    } else if (container.mounts.every(path => /^(\/var\/run\/docker\.sock|\/run\/docker\.sock|\/etc\/localtime|\/etc\/timezone)$/.test(path))) {
      group = 'unavailable'; reason = '仅发现运行时套接字或时区挂载，没有识别到业务数据卷。'
    } else if (names) { group = 'review'; reason = `已有项目使用此容器：${names}。请核对是否包含数据卷，避免重复配置。` }
    else if (!container.running) { group = 'review'; reason = '容器已停止，但持久化挂载仍可生成备份草稿；请确认它仍需要保留。' }
    else if (/^(nginx|openresty|traefik|caddy|adminer|phpmyadmin)$/.test(imageName(container.image))) {
      group = 'review'; reason = '入口或管理服务，挂载可能仅包含配置；按恢复需求选择。'
    } else if (/^(redis|valkey|mongo|mongodb|elasticsearch|opensearch|rabbitmq)$/.test(imageName(container.image))) {
      group = 'review'; reason = '有状态服务不能仅凭挂载判断备份一致性；请先确认原生导出、刷盘或停写方案。'
    }
    add('containers', index, container.name, `${container.image} · ${container.running ? '运行中' : '已停止'} · ${container.mounts?.length ?? 0} 个挂载`, '容器', group, reason)
  }
  return candidates
}

export function selectedDetectionCandidates(candidates: DetectionCandidate[], selection: DetectionSelection): DetectionCandidate[] {
  return candidates.filter((candidate) => candidate.selectable && selection[candidate.kind].includes(candidate.index))
}
