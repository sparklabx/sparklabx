// Dev-only logging helpers. Use in place of bare console.log so verbose
// kernel/notebook traces do not ship to users.
// console.warn and console.error are intentionally not wrapped — they surface
// real issues and are useful in production monitoring.

const isDev = import.meta.env.DEV;

export function devLog(...args: unknown[]): void {
    if (isDev) {
        console.log(...args);
    }
}

export function devInfo(...args: unknown[]): void {
    if (isDev) {
        console.info(...args);
    }
}
