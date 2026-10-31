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
        live: token('live'),
        'warn-text': token('warn-text'),
      },
      borderColor: {
        DEFAULT: token('line'),
      },
      fontFamily: {
        sans: ['"IBM Plex Sans"', 'ui-sans-serif', 'system-ui', 'sans-serif'],
        mono: ['var(--font-mono)'],
      },
      letterSpacing: {
        label: '0.08em',
      },
    },
  },
  plugins: [],
}
