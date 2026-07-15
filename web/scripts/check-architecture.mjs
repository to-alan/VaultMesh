import { readdir, readFile } from 'node:fs/promises'
import { dirname, extname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const webRoot = dirname(dirname(fileURLToPath(import.meta.url)))
const sourceRoot = join(webRoot, 'src')
const violations = []

async function sourceFiles(directory) {
  const entries = await readdir(directory, { withFileTypes: true })
  const nested = await Promise.all(entries.map(async (entry) => {
    const path = join(directory, entry.name)
    if (entry.isDirectory()) return sourceFiles(path)
    return ['.ts', '.vue'].includes(extname(entry.name)) ? [path] : []
  }))
  return nested.flat()
}

for (const path of await sourceFiles(sourceRoot)) {
  const name = relative(webRoot, path).replaceAll('\\', '/')
  const source = await readFile(path, 'utf8')
  const isService = name.startsWith('src/services/')
  const isTransport = name === 'src/api.ts'
  const isBootstrap = name === 'src/bootstrap.ts'

  if (/['"`]\/api\/v\d/.test(source) && !isService) {
    violations.push(`${name}: versioned API paths may only be declared in src/services`)
  }
  if (/\brequestJSON\s*(?:<[^>]+>)?\s*\(/.test(source) && !isService && !isTransport) {
    violations.push(`${name}: requestJSON may only be used by the typed service layer`)
  }
  if (/\bfetch\s*\(/.test(source) && !isTransport && !isBootstrap) {
    violations.push(`${name}: direct fetch is restricted to api.ts and bootstrap.ts`)
  }
}

if (violations.length) {
  process.stderr.write(`Frontend architecture check failed:\n- ${violations.join('\n- ')}\n`)
  process.exitCode = 1
} else {
  process.stdout.write('Frontend architecture boundaries are valid.\n')
}
