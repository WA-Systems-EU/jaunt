const path = require('path');

// Same theme as the inline config the pages used with the Tailwind play CDN.
// darkMode is left at the default ('media'); the pages' CSS variables handle dark mode.
module.exports = {
  content: [path.join(__dirname, '../site/**/*.html')],
  theme: {
    extend: {
      colors: {
        bg: 'var(--bg)', surface: 'var(--surface)', ink: 'var(--ink)', muted: 'var(--muted)',
        line: 'var(--line)', route: 'var(--route)', tag: 'var(--tag)', tagink: 'var(--tag-ink)',
        board: 'var(--board)', boardink: 'var(--board-ink)', ok: 'var(--ok)', warn: 'var(--warn)'
      },
      fontFamily: {
        display: ['"Big Shoulders Display"', 'Impact', '"Arial Narrow"', 'sans-serif'],
        sans: ['"Public Sans"', 'system-ui', '-apple-system', 'Segoe UI', 'sans-serif'],
        mono: ['"JetBrains Mono"', 'ui-monospace', 'SFMono-Regular', 'Menlo', 'monospace']
      }
    }
  }
};
