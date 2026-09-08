import type { DetectionReport } from '../types'

export const detectionFixture: DetectionReport = {
  generated_at: '2026-09-08T03:00:00Z', tools: { restic: 'restic 0.18.0', docker: 'Docker 28' },
  apps: [
    { path: '/opt/vaultmesh', name: 'Docker Compose', kind: 'compose', markers: ['compose.yaml'], exclusion_reason: 'VaultMesh 自身组件；请使用专门的平台灾备方案' },
    { path: '/srv/shop', name: 'Node.js', kind: 'nodejs', markers: ['package.json'] },
    { path: '/srv/shop/api', name: 'Node.js', kind: 'nodejs', markers: ['package.json'] },
    { path: '/srv/application', name: 'PHP', kind: 'php', markers: ['index.php'] },
  ],
  databases: [
    { kind: 'mysql', source: 'docker', container: 'shop-mysql', host: '127.0.0.1', port: 13306, reachable: true, dump_tool: 'mysqldump 8.4' },
    { kind: 'postgresql', source: 'docker', container: 'analytics-db', host: '127.0.0.1', port: 0, reachable: false },
    { kind: 'postgresql', source: 'docker', container: 'vaultmesh-postgres-1', host: '127.0.0.1', port: 0, reachable: false, exclusion_reason: 'VaultMesh 自身数据库' },
  ],
  containers: [
    { name: 'shop-uploads', image: 'shop/uploads:latest', running: true, mounts: ['/srv/shop/uploads'] },
    { name: 'shop-mysql', image: 'mysql:8.4', running: true, mounts: ['/data/mysql'] },
    { name: 'gateway', image: 'nginx:alpine', running: true, mounts: ['/etc/nginx'] },
    { name: 'worker', image: 'shop/worker:latest', running: true },
    { name: 'vaultmesh-control-1', image: 'ghcr.io/to-alan/vaultmesh/vaultmesh-control:edge-test', running: true },
  ],
}
