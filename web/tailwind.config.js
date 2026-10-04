/** @type {import('tailwindcss').Config} */
const token = (name) => `rgb(var(--${name}) / <alpha-value>)`

export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        paper: token('paper'),
        surface: token('surface'),
        code: token('code'),
        ink: token('ink'),
        body: token('body'),
        sec: token('sec'),
        mute: token('mute'),
        line: token('line'),
        'line-strong': token('line-strong'),
        accent: token('accent'),
        danger: token('danger'),
        warn: token('warn'),
        'warn-text': token('warn-text'),
      },
      borderColor: {
        DEFAULT: token('line'),
      },
      fontFamily: {
        sans: ['Inter', 'ui-sans-serif', 'system-ui', 'sans-serif'],
        mono: ['"IBM Plex Mono"', 'ui-monospace', 'SFMono-Regular', 'Menlo', 'monospace'],
      },
      letterSpacing: {
        label: '0.08em',
      },
    },
  },
  plugins: [],
}
