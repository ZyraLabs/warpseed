import React from 'react'
import {createRoot} from 'react-dom/client'
import './style.css'
import App from './App'
import {applyMirroredTheme} from './lib/theme'

// Stamp the theme before first render — no flash (design-agent spec §4).
applyMirroredTheme()

const container = document.getElementById('root')

const root = createRoot(container!)

root.render(
    <React.StrictMode>
        <App/>
    </React.StrictMode>
)
