import fs from 'fs'
import zh from './src/locales/zh.json' with { type: 'json' }
import en from './src/locales/en.json' with { type: 'json' }

const files = [
  './src/pages/Accounts.tsx',
  './src/components/AccountUsageModal.tsx',
  './src/components/Sub2APIImportModal.tsx',
  './src/components/Layout.tsx',
]

function resolve(root, path) {
  let o = root
  for (const p of path.split('.')) {
    if (o == null || typeof o !== 'object' || !(p in o)) return { ok: false }
    o = o[p]
  }
  return { ok: true, value: o }
}

const keyRe = /\bt\(\s*['"]([a-zA-Z0-9_.]+)['"]/g
const keys = new Set()
for (const f of files) {
  const src = fs.readFileSync(f, 'utf8')
  for (const m of src.matchAll(keyRe)) keys.add(m[1])
}

const missingZh = []
const missingEn = []
const objectZh = []
const objectEn = []
for (const k of [...keys].sort()) {
  const z = resolve(zh, k)
  const e = resolve(en, k)
  if (!z.ok) missingZh.push(k)
  else if (typeof z.value !== 'string') objectZh.push([k, typeof z.value])
  if (!e.ok) missingEn.push(k)
  else if (typeof e.value !== 'string') objectEn.push([k, typeof e.value])
}

console.log('keys scanned', keys.size)
console.log('missing zh', missingZh)
console.log('missing en', missingEn)
console.log('object zh', objectZh)
console.log('object en', objectEn)