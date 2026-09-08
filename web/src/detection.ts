import type { DetectionReport, DetectionStatus } from './types'

export function reportMatchesDetectionCommand(report: DetectionReport | undefined, commandID: string): report is DetectionReport {
  return Boolean(commandID && report?.command_id === commandID)
}

export type DetectionPollDecision =
  | { kind: 'complete'; report: DetectionReport }
  | { kind: 'superseded' }
  | { kind: 'pending' }

// The latest command is authoritative. If another console has queued a newer
// scan, an older page must stop polling instead of eventually presenting a
// report that is already stale relative to the server's current request.
export function detectionPollDecision(status: DetectionStatus, commandID: string): DetectionPollDecision {
  if (status.command && status.command.id !== commandID) return { kind: 'superseded' }
  if (status.available && reportMatchesDetectionCommand(status.report, commandID)) {
    return { kind: 'complete', report: status.report }
  }
  return { kind: 'pending' }
}

export function supportsDetectionVersion(agentVersion: string): boolean {
  const channel = agentVersion.trim().toLowerCase()
  if (channel === 'dev' || channel === 'edge' || channel.startsWith('edge-')) return true
  const match = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+([0-9A-Za-z.-]+))?$/.exec(agentVersion.trim())
  if (!match) return false
  const numbers = match.slice(1, 4).map(Number)
  if (numbers.some((number) => !Number.isSafeInteger(number))) return false
  const minimum = [0, 1, 2]
  for (let index = 0; index < numbers.length; index += 1) {
    if (numbers[index] !== minimum[index]) return numbers[index] > minimum[index]
  }
  return !match[4]
}

export function buildAgentInstallCommand(apiBaseURL: string, enrollmentToken: string, controlPlaneVersion: string): string {
  const parsedAPIURL = new URL(apiBaseURL)
  const isRemotePlainHTTP = agentInstallUsesLoopbackFallback(parsedAPIURL)
  const agentURL = isRemotePlainHTTP ? 'http://localhost:8080' : apiBaseURL
  const normalizedVersion = controlPlaneVersion.toLowerCase()
  const isRelease = /^v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)(?:-rc\.(?:0|[1-9]\d*))?$/.test(controlPlaneVersion)
  const edgeVersion = /^edge-[a-f0-9]{40}$/.test(normalizedVersion) ? normalizedVersion : 'edge'
  const selectedVersion = isRelease ? controlPlaneVersion : edgeVersion
  const installerURL = isRelease
    ? `https://github.com/to-alan/VaultMesh/releases/download/${selectedVersion}/install.sh`
    : `https://raw.githubusercontent.com/to-alan/VaultMesh/${edgeVersion === 'edge' ? 'main' : edgeVersion.slice(5)}/install.sh`
  // Download before executing; stable installations never consume mutable main.
  return `curl -fL ${installerURL} -o vaultmesh-install.sh && sudo sh vaultmesh-install.sh install-agent ${shellQuote(agentURL)} ${shellQuote(enrollmentToken)} --version ${shellQuote(selectedVersion)}`
}

export function agentInstallUsesLoopbackFallback(apiBaseURL: string | URL): boolean {
  const parsedAPIURL = typeof apiBaseURL === 'string' ? new URL(apiBaseURL) : apiBaseURL
  const loopbackHosts = new Set(['localhost', '127.0.0.1', '[::1]'])
  return parsedAPIURL.protocol === 'http:' && !loopbackHosts.has(parsedAPIURL.hostname)
}

export function shellQuote(value: string): string {
  return `'${value.replaceAll("'", `'"'"'`)}'`
}
