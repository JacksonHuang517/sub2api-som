/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{vue,js,ts,jsx,tsx}'],
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        // SOM AI HUB: neutral surfaces with restrained blue interactions.
        primary: {
          50: '#eff6ff', 100: '#dbeafe', 200: '#bfdbfe', 300: '#93c5fd',
          400: '#0a84ff', 500: '#4285f4', 600: '#007aff', 700: '#0062cc',
          800: '#17467b', 900: '#15304f', 950: '#101c2c'
        },
        gray: {
          50: '#f5f5f7', 100: '#ebebef', 200: '#dedee3', 300: '#c7c7cd',
          400: '#a1a1a6', 500: '#6e6e73', 600: '#55555b', 700: '#3a3a3c',
          800: '#2c2c2e', 900: '#1d1d1f', 950: '#0e0e10'
        },
        accent: {
          50: '#f5f5f7', 100: '#ebebef', 200: '#dedee3', 300: '#c7c7cd',
          400: '#a1a1a6', 500: '#6e6e73', 600: '#55555b', 700: '#3a3a3c',
          800: '#2c2c2e', 900: '#1c1c1e', 950: '#0e0e10'
        },
        dark: {
          50: '#f5f5f7', 100: '#e8e8ed', 200: '#d5d5da', 300: '#bcbcc2',
          400: '#a1a1a6', 500: '#6e6e73', 600: '#3a3a3c', 700: '#2c2c2e',
          800: '#1c1c1e', 900: '#1c1c1e', 950: '#0e0e10'
        }
      },
      fontFamily: {
        sans: [
          'system-ui',
          '-apple-system',
          'BlinkMacSystemFont',
          'Segoe UI',
          'Roboto',
          'Helvetica Neue',
          'Arial',
          'PingFang SC',
          'Hiragino Sans GB',
          'Microsoft YaHei',
          'sans-serif'
        ],
        mono: ['ui-monospace', 'SFMono-Regular', 'Menlo', 'Monaco', 'Consolas', 'monospace']
      },
      boxShadow: {
        glass: '0 8px 32px rgba(0, 0, 0, 0.08)',
        'glass-sm': '0 4px 16px rgba(0, 0, 0, 0.06)',
        glow: '0 0 20px rgba(66, 133, 244, 0.25)',
        'glow-lg': '0 0 40px rgba(66, 133, 244, 0.35)',
        card: '0 1px 3px rgba(0, 0, 0, 0.04), 0 1px 2px rgba(0, 0, 0, 0.06)',
        'card-hover': '0 10px 40px rgba(0, 0, 0, 0.08)',
        'inner-glow': 'inset 0 1px 0 rgba(255, 255, 255, 0.1)'
      },
      backgroundImage: {
        'gradient-radial': 'radial-gradient(var(--tw-gradient-stops))',
        'gradient-primary': 'linear-gradient(135deg, #4285f4 0%, #007aff 100%)',
        'gradient-dark': 'linear-gradient(135deg, #1c1c1e 0%, #0e0e10 100%)',
        'gradient-glass':
          'linear-gradient(135deg, rgba(255,255,255,0.1) 0%, rgba(255,255,255,0.05) 100%)',
        'mesh-gradient':
          'radial-gradient(at 40% 20%, rgba(66, 133, 244, 0.12) 0px, transparent 50%), radial-gradient(at 80% 0%, rgba(255, 107, 53, 0.08) 0px, transparent 50%), radial-gradient(at 0% 50%, rgba(66, 133, 244, 0.08) 0px, transparent 50%)'
      },
      animation: {
        'fade-in': 'fadeIn 0.3s ease-out',
        'slide-up': 'slideUp 0.3s ease-out',
        'slide-down': 'slideDown 0.3s ease-out',
        'slide-in-right': 'slideInRight 0.3s ease-out',
        'scale-in': 'scaleIn 0.2s ease-out',
        'pulse-slow': 'pulse 3s cubic-bezier(0.4, 0, 0.6, 1) infinite',
        shimmer: 'shimmer 2s linear infinite',
        glow: 'glow 2s ease-in-out infinite alternate'
      },
      keyframes: {
        fadeIn: {
          '0%': { opacity: '0' },
          '100%': { opacity: '1' }
        },
        slideUp: {
          '0%': { opacity: '0', transform: 'translateY(10px)' },
          '100%': { opacity: '1', transform: 'translateY(0)' }
        },
        slideDown: {
          '0%': { opacity: '0', transform: 'translateY(-10px)' },
          '100%': { opacity: '1', transform: 'translateY(0)' }
        },
        slideInRight: {
          '0%': { opacity: '0', transform: 'translateX(20px)' },
          '100%': { opacity: '1', transform: 'translateX(0)' }
        },
        scaleIn: {
          '0%': { opacity: '0', transform: 'scale(0.95)' },
          '100%': { opacity: '1', transform: 'scale(1)' }
        },
        shimmer: {
          '0%': { backgroundPosition: '-200% 0' },
          '100%': { backgroundPosition: '200% 0' }
        },
        glow: {
          '0%': { boxShadow: '0 0 20px rgba(66, 133, 244, 0.25)' },
          '100%': { boxShadow: '0 0 30px rgba(66, 133, 244, 0.4)' }
        }
      },
      backdropBlur: {
        xs: '2px'
      },
      borderRadius: {
        '4xl': '2rem'
      }
    }
  },
  plugins: []
}
