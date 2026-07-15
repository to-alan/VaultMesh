export function suggestedPasskeyName(): string {
  const nav = navigator as Navigator & { userAgentData?: { platform?: string } }
  const platform = nav.userAgentData?.platform || navigator.platform || '当前设备'
  return `${platform} · ${new Intl.DateTimeFormat('zh-CN', { month: 'short', day: 'numeric' }).format(new Date())}`
}

export function friendlyPasskeyError(cause: unknown, rpID: string): Error {
  if (!(cause instanceof DOMException)) return cause instanceof Error ? cause : new Error('通行密钥注册失败')
  if (cause.name === 'InvalidStateError') return new Error('这台设备已经为 VaultMesh 注册过通行密钥，请使用现有密钥或换一台设备。')
  if (cause.name === 'SecurityError') return new Error(`通行密钥域名校验失败：当前页面是 ${window.location.origin}，服务器 RP ID 是 ${rpID || '未配置'}。`)
  if (cause.name === 'NotAllowedError') return new Error('系统没有完成通行密钥创建：可能是你取消了操作、设备未设置锁屏，或浏览器等待超时。')
  return new Error(`通行密钥注册失败：${cause.message || cause.name}`)
}

export function parseCreationOptions(input: any): PublicKeyCredentialCreationOptions {
  return {
    ...input,
    challenge: base64urlToBuffer(input.challenge),
    user: { ...input.user, id: base64urlToBuffer(input.user.id) },
    excludeCredentials: (input.excludeCredentials ?? []).map((item: any) => ({ ...item, id: base64urlToBuffer(item.id) })),
  }
}

export function parseRequestOptions(input: any): PublicKeyCredentialRequestOptions {
  return {
    ...input,
    challenge: base64urlToBuffer(input.challenge),
    allowCredentials: (input.allowCredentials ?? []).map((item: any) => ({ ...item, id: base64urlToBuffer(item.id) })),
  }
}

export function serializeRegistration(credential: PublicKeyCredential) {
  const response = credential.response as AuthenticatorAttestationResponse
  return {
    id: credential.id,
    rawId: bufferToBase64url(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      clientDataJSON: bufferToBase64url(response.clientDataJSON),
      attestationObject: bufferToBase64url(response.attestationObject),
      transports: typeof response.getTransports === 'function' ? response.getTransports() : [],
    },
  }
}

export function serializeAssertion(credential: PublicKeyCredential) {
  const response = credential.response as AuthenticatorAssertionResponse
  return {
    id: credential.id,
    rawId: bufferToBase64url(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      clientDataJSON: bufferToBase64url(response.clientDataJSON),
      authenticatorData: bufferToBase64url(response.authenticatorData),
      signature: bufferToBase64url(response.signature),
      userHandle: bufferToBase64url(response.userHandle),
    },
  }
}

function base64urlToBuffer(value: string): ArrayBuffer {
  const normalized = value.replace(/-/g, '+').replace(/_/g, '/')
  const padded = normalized + '='.repeat((4 - normalized.length % 4) % 4)
  const binary = atob(padded)
  return Uint8Array.from(binary, (character) => character.charCodeAt(0)).buffer
}

function bufferToBase64url(value: ArrayBuffer | null): string | null {
  if (!value) return null
  const bytes = new Uint8Array(value)
  let binary = ''
  bytes.forEach((byte) => { binary += String.fromCharCode(byte) })
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}
