/**
 * Data Utilities for file parsing and formatting
 */

// Parse CSV text into headers + rows
export function parseCsv(text: string): { headers: string[]; rows: string[][] } {
    const lines = text.trim().split('\n');
    if (lines.length === 0) return { headers: [], rows: [] };
    const parse = (line: string) => {
        const result: string[] = [];
        let current = '', inQuotes = false;
        for (let i = 0; i < line.length; i++) {
            const ch = line[i];
            if (ch === '"') { inQuotes = !inQuotes; }
            else if (ch === ',' && !inQuotes) { result.push(current.trim()); current = ''; }
            else { current += ch; }
        }
        result.push(current.trim());
        return result;
    };
    const headers = parse(lines[0]);
    const rows = lines.slice(1).filter(l => l.trim()).map(parse);
    return { headers, rows };
}

// Parse JSON into headers + rows (array of objects)
export function parseJsonTable(text: string): { headers: string[]; rows: string[][] } | null {
    try {
        let data = JSON.parse(text);
        if (!Array.isArray(data)) {
            // Try common wrappers
            if (data.data && Array.isArray(data.data)) data = data.data;
            else if (data.results && Array.isArray(data.results)) data = data.results;
            else return null;
        }
        if (data.length === 0 || typeof data[0] !== 'object') return null;
        const headers = Object.keys(data[0]);
        const rows = data.map((row: any) => headers.map(h => String(row[h] ?? '')));
        return { headers, rows };
    } catch { return null; }
}

// Format file size
export function formatSize(bytes: number): string {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
}
