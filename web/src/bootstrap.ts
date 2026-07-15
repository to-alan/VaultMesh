async function startApplication() {
  try {
    const response = await fetch('/config.txt', { cache: 'no-store', credentials: 'same-origin' })
    if (response.ok) {
      const apiBaseUrl = (await response.text()).trim()
      if (apiBaseUrl) window.__VAULTMESH_CONFIG__ = { apiBaseUrl }
    }
  } catch {
    // Development and custom static deployments can use VITE_API_BASE_URL.
  }
  await import('./main')
}

void startApplication()
