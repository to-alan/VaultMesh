import { describe, expect, it } from 'vitest'
import { agentInstallUsesLoopbackFallback, buildAgentInstallCommand, detectionPollDecision, reportMatchesDetectionCommand, shellQuote, supportsDetectionVersion } from '../detection'

describe('detection command correlation', () => {
  it('accepts only the report produced by the active command', () => {
    expect(reportMatchesDetectionCommand({ generated_at: '2026-09-04T00:00:00Z', command_id: 'cmd_active' }, 'cmd_active')).toBe(true)
    expect(reportMatchesDetectionCommand({ generated_at: '2026-09-04T00:00:01Z', command_id: 'cmd_other' }, 'cmd_active')).toBe(false)
    expect(reportMatchesDetectionCommand({ generated_at: '2026-09-04T00:00:02Z' }, 'cmd_active')).toBe(false)
  })

  it('stops an older poll when a newer command becomes authoritative', () => {
    expect(detectionPollDecision({
      available: true,
      has_command: true,
      command: { id: 'cmd_new', attempts: 1, created_at: '2026-09-04T00:01:00Z' },
      report: { generated_at: '2026-09-04T00:00:30Z', command_id: 'cmd_old' },
    }, 'cmd_old')).toEqual({ kind: 'superseded' })
  })

  it('completes only from the report belonging to the active command', () => {
    const report = { generated_at: '2026-09-04T00:00:30Z', command_id: 'cmd_active' }
    expect(detectionPollDecision({ available: true, has_command: true, report }, 'cmd_active')).toEqual({ kind: 'complete', report })
    expect(detectionPollDecision({ available: true, has_command: true, report: { ...report, command_id: 'cmd_old' } }, 'cmd_active')).toEqual({ kind: 'pending' })
  })
})

describe('detection version support', () => {
  it.each([
    ['v0.1.1', false],
    ['v0.0.99', false],
    ['v0.1.2-rc.1', false],
    ['v0.1.2', true],
    ['v0.1.2+build.7', true],
    ['v0.2.0', true],
    ['v1.0.0', true],
    ['edge-20260904', true],
    ['dev', true],
    ['', false],
    ['unknown', false],
    ['v0.1.2-', false],
    ['v999999999999999999999.0.0', false],
  ])('classifies %s as supported=%s', (version, expected) => {
    expect(supportsDetectionVersion(version)).toBe(expected)
  })
})

describe('Agent install command', () => {
  it('rewrites remote plain HTTP and pins edge agents to an edge control plane', () => {
    const command = buildAgentInstallCommand('http://192.0.2.8:8080', 'enroll_test', 'edge-dev')
    expect(command).toContain("--version 'edge'")
    expect(command).toContain("install-agent 'http://localhost:8080' 'enroll_test'")
  })

  it('recognizes edge channel names case-insensitively', () => {
    expect(buildAgentInstallCommand('https://backup.example.com', 'enroll_test', 'EDGE-dev'))
      .toContain("--version 'edge'")
  })

  it('marks only remote plain HTTP addresses as same-host fallbacks', () => {
    expect(agentInstallUsesLoopbackFallback('http://192.0.2.8:8080')).toBe(true)
    expect(agentInstallUsesLoopbackFallback('http://localhost:8080')).toBe(false)
    expect(agentInstallUsesLoopbackFallback('http://[::1]:8080')).toBe(false)
    expect(agentInstallUsesLoopbackFallback('https://backup.example.com')).toBe(false)
  })

  it('keeps HTTPS URLs and shell-quotes every argument', () => {
    const command = buildAgentInstallCommand('https://backup.example.com', "enroll_a'b", 'v0.1.2')
    expect(command).toContain("install-agent 'https://backup.example.com'")
    expect(command).toContain(shellQuote("enroll_a'b"))
    expect(command).not.toContain('VAULTMESH_AGENT_VERSION=edge')
    expect(command).toContain('/releases/download/v0.1.2/install.sh')
    expect(command).toContain("--version 'v0.1.2'")
    expect(command).not.toContain('/main/')
  })

  it('pins edge installer and binary to the same full revision', () => {
    const revision = 'a'.repeat(40)
    const command = buildAgentInstallCommand('https://backup.example.com', 'enroll_test', `edge-${revision}`)
    expect(command).toContain(`/VaultMesh/${revision}/install.sh`)
    expect(command).toContain(`--version 'edge-${revision}'`)
    expect(command).not.toContain('/main/')
  })

  it('pins release candidates without falling back to main', () => {
    expect(buildAgentInstallCommand('https://backup.example.com', 'enroll_test', 'v0.2.0-rc.1'))
      .toContain('/releases/download/v0.2.0-rc.1/install.sh')
  })
})

describe('shellQuote', () => {
  it('escapes embedded single quotes without opening the shell argument', () => {
    expect(shellQuote("a'b")).toBe(`'a'"'"'b'`)
  })
})
