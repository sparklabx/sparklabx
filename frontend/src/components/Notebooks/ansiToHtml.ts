/**
 * Convert ANSI escape codes to HTML for notebook output display
 */

// ANSI color codes mapping
const ANSI_COLORS: Record<number, string> = {
    30: '#000000', // Black
    31: '#ef4444', // Red
    32: '#22c55e', // Green
    33: '#fde047', // Yellow (lighter)
    34: '#3b82f6', // Blue
    35: '#a855f7', // Magenta
    36: '#06b6d4', // Cyan
    37: '#ffffff', // White
    39: 'inherit', // Default
    90: '#6b7280', // Bright Black (Gray)
    91: '#f87171', // Bright Red
    92: '#4ade80', // Bright Green
    93: '#fef08a', // Bright Yellow (lighter)
    94: '#60a5fa', // Bright Blue
    95: '#c084fc', // Bright Magenta
    96: '#22d3ee', // Bright Cyan
    97: '#ffffff', // Bright White
};

const ANSI_BG_COLORS: Record<number, string> = {
    40: '#000000',
    41: '#ef4444',
    42: '#22c55e',
    43: '#eab308',
    44: '#3b82f6',
    45: '#a855f7',
    46: '#06b6d4',
    47: '#ffffff',
    49: 'transparent',
};

/**
 * Convert ANSI escape codes to styled HTML spans
 */
export function ansiToHtml(text: string): string {
    if (!text) return '';

    // Escape HTML entities first
    const html = text
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;');

    // Match ANSI escape sequences in multiple formats:
    // - \x1b[ (actual escape char)
    // - [ followed by codes and 'm' (literal brackets from some terminals)
    // Also handle 256-color format: 38;5;X or 48;5;X
    // eslint-disable-next-line no-control-regex -- ANSI escape sequences require \x1b
    const ansiRegex = /(?:\x1b\[|\[)([0-9;]*)m/g;

    let result = '';
    let lastIndex = 0;
    let currentStyles: string[] = [];
    let match;

    while ((match = ansiRegex.exec(html)) !== null) {
        // Add text before this match
        result += html.slice(lastIndex, match.index);
        lastIndex = match.index + match[0].length;

        const codesStr = match[1];
        const codes = codesStr.split(';').map(c => parseInt(c, 10) || 0);

        let i = 0;
        while (i < codes.length) {
            const code = codes[i];

            if (code === 0) {
                // Reset all styles - close any open spans
                if (currentStyles.length > 0) {
                    result += '</span>'.repeat(currentStyles.length);
                    currentStyles = [];
                }
            } else if (code === 1) {
                // Bold
                result += '<span style="font-weight:bold">';
                currentStyles.push('bold');
            } else if (code === 3) {
                // Italic
                result += '<span style="font-style:italic">';
                currentStyles.push('italic');
            } else if (code === 4) {
                // Underline
                result += '<span style="text-decoration:underline">';
                currentStyles.push('underline');
            } else if (code === 38 && codes[i + 1] === 5) {
                // 256-color foreground: 38;5;X
                const colorCode = codes[i + 2] || 0;
                const color = get256Color(colorCode);
                result += `<span style="color:${color}">`;
                currentStyles.push('color');
                i += 2; // Skip 5 and color code
            } else if (code === 48 && codes[i + 1] === 5) {
                // 256-color background: 48;5;X
                const colorCode = codes[i + 2] || 0;
                const color = get256Color(colorCode);
                result += `<span style="background-color:${color}">`;
                currentStyles.push('bgcolor');
                i += 2; // Skip 5 and color code
            } else if (code >= 30 && code <= 37 || code >= 90 && code <= 97 || code === 39) {
                // Foreground colors
                const color = ANSI_COLORS[code] || 'inherit';
                result += `<span style="color:${color}">`;
                currentStyles.push('color');
            } else if (code >= 40 && code <= 47 || code === 49) {
                // Background colors
                const bgColor = ANSI_BG_COLORS[code] || 'transparent';
                result += `<span style="background-color:${bgColor}">`;
                currentStyles.push('bgcolor');
            }
            i++;
        }
    }

    // Add remaining text
    result += html.slice(lastIndex);

    // Close any remaining open spans
    if (currentStyles.length > 0) {
        result += '</span>'.repeat(currentStyles.length);
    }

    // Convert newlines to <br>
    result = result.replace(/\n/g, '<br>');

    return result;
}

/**
 * Get color for 256-color palette
 */
function get256Color(code: number): string {
    // Standard colors (0-15)
    const standardColors = [
        '#000000', '#cd0000', '#00cd00', '#cdcd00', '#0000ee', '#cd00cd', '#00cdcd', '#e5e5e5',
        '#7f7f7f', '#ff0000', '#00ff00', '#ffff00', '#5c5cff', '#ff00ff', '#00ffff', '#ffffff'
    ];

    if (code < 16) {
        return standardColors[code] || '#ffffff';
    }

    // Color cube (16-231): 6x6x6 cube
    if (code < 232) {
        const idx = code - 16;
        const r = Math.floor(idx / 36);
        const g = Math.floor((idx % 36) / 6);
        const b = idx % 6;
        const toHex = (v: number) => (v === 0 ? 0 : 55 + v * 40).toString(16).padStart(2, '0');
        return `#${toHex(r)}${toHex(g)}${toHex(b)}`;
    }

    // Grayscale (232-255)
    const gray = 8 + (code - 232) * 10;
    const hex = gray.toString(16).padStart(2, '0');
    return `#${hex}${hex}${hex}`;
}

/**
 * Strip ANSI codes from text (for plain text display)
 */
export function stripAnsi(text: string): string {
    if (!text) return '';
    // eslint-disable-next-line no-control-regex -- ANSI escape sequences require \x1b
    return text.replace(/\x1b\[[0-9;]*m/g, '');
}
