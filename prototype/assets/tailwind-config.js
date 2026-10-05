// PROTOTYPE: maps theme.css tokens into Tailwind (Play CDN v3). Load right after cdn.tailwindcss.com.
const tok = (name) => `rgb(var(--${name}) / <alpha-value>)`;
tailwind.config = {
  theme: {
    extend: {
      colors: {
        bg: tok('bg'),
        surface: tok('surface'),
        raised: tok('raised'),
        line: tok('line'),
        ink: tok('ink'),
        muted: tok('muted'),
        accent: tok('accent'),
        'accent-ink': tok('accent-ink'),
        ok: tok('ok'),
        bad: tok('bad'),
        warn: tok('warn'),
        info: tok('info'),
      },
      fontFamily: {
        sans: ['"IBM Plex Sans"', 'ui-sans-serif', 'sans-serif'],
        mono: ['"IBM Plex Mono"', 'ui-monospace', 'monospace'],
      },
      borderRadius: {
        sm: 'var(--r-sm)',
        md: 'var(--r-md)',
        lg: 'var(--r-lg)',
      },
    },
  },
};
