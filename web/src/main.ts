import { createApp } from 'vue'
import App from './App.vue'
import './style.css'

const app = createApp(App)
// Template event handlers that throw must never vanish silently: surface
// them through the same banner the rest of the UI reads.
app.config.errorHandler = (err) => {
  const detail = err instanceof Error ? err.message : String(err)
  window.dispatchEvent(new CustomEvent('vaultmesh-ui-error', { detail }))
  console.error(err)
}
app.mount('#app')
