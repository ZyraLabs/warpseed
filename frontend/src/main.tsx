import React from 'react'
import {createRoot} from 'react-dom/client'
import '@fontsource/instrument-sans/latin-400.css'
import '@fontsource/instrument-sans/latin-500.css'
import '@fontsource/instrument-sans/latin-600.css'
import '@fontsource/instrument-sans/latin-700.css'
import '@fontsource/bricolage-grotesque/latin-600.css'
import '@fontsource/bricolage-grotesque/latin-700.css'
import './style.css'
import App from './App'
import {applyMirroredTheme} from './lib/theme'

// Stamp the theme before first render — no flash (design-agent spec §4).
applyMirroredTheme()

// Browser-only demo: `?mock=1` (or VITE_MOCK=1) swaps the Wails bridge for
// an in-page mock so the UI runs in a plain browser (docs screenshots,
// design work). The chunk is only ever loaded when the flag is set.
const wantMock =
    new URLSearchParams(window.location.search).get('mock') === '1' ||
    import.meta.env.VITE_MOCK === '1'

const boot = wantMock
    ? import('./mock').then(({installMock}) => installMock())
    : Promise.resolve()

void boot.then(() => {
    const container = document.getElementById('root')
    const root = createRoot(container!)
    root.render(
        <React.StrictMode>
            <App/>
        </React.StrictMode>
    )
})
