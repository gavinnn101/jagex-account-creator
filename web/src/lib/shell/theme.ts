/** The two palettes of app.css. Dark is the default. */
export type Theme = 'dark' | 'light';

/**
 * The localStorage key of the theme choice. The head script of app.html reads
 * the same key before the first paint.
 * Limitation: blocked storage gives dark at each load, whatever the last choice.
 */
export const themeKey = 'creator-theme';

/** Returns the theme on screen, from the data-theme attribute of root. */
export function currentTheme(root: HTMLElement = document.documentElement): Theme {
  return root.dataset.theme === 'light' ? 'light' : 'dark';
}

/** Shows theme on root and stores the choice for the next load. */
export function applyTheme(theme: Theme, root: HTMLElement = document.documentElement): void {
  root.dataset.theme = theme;
  try {
    localStorage.setItem(themeKey, theme);
  } catch {
    // Blocked storage keeps the choice for this page only.
  }
}
