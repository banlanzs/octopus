export const SW_MESSAGE_TYPE = {
    SKIP_WAITING: 'SKIP_WAITING',
    CLEAR_CACHE: 'CLEAR_CACHE',
    CACHE_CLEARED: 'CACHE_CLEARED',
} as const;

// Keep in sync with `web/public/sw.js`
export const OCTOPUS_CACHE_PREFIX = 'octopus-';
// Font cache is version-independent and should persist across updates
export const OCTOPUS_FONT_CACHE_NAME = 'octopus-font';

export function isOctopusCacheName(name: string) {
    return name.startsWith(OCTOPUS_CACHE_PREFIX);
}

export function isFontCacheName(name: string) {
    return name === OCTOPUS_FONT_CACHE_NAME;
}


