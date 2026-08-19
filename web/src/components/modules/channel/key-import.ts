export type ParsedChannelKeyImport = {
    keys: string[];
    duplicated: number;
};

function normalizeKeyToken(raw: string): string {
    let key = raw.trim();
    if (key.length >= 2) {
        const first = key[0];
        const last = key[key.length - 1];
        if (
            (first === '"' && last === '"') ||
            (first === "'" && last === "'") ||
            (first === '`' && last === '`')
        ) {
            key = key.slice(1, -1).trim();
        }
    }
    if (/^Bearer\s+/i.test(key)) {
        key = key.replace(/^Bearer\s+/i, '').trim();
    }
    return key;
}

/**
 * 解析批量导入文本。支持：
 * - 每行一个 key，或逗号 / 分号 / Tab 分隔
 * - 同一行无分隔符时按空白分隔
 * - JSON 字符串数组，如 ["sk-a","sk-b"]
 * 返回去重后的 keys 和重复项数量。
 */
export function parseChannelKeyImportContent(content: string): ParsedChannelKeyImport {
    const keys: string[] = [];
    const seen = new Set<string>();
    let duplicated = 0;

    const add = (raw: string) => {
        const key = normalizeKeyToken(raw);
        if (!key) return;
        if (seen.has(key)) {
            duplicated += 1;
            return;
        }
        seen.add(key);
        keys.push(key);
    };

    const trimmed = content.trim();
    if (trimmed.startsWith('[')) {
        try {
            const parsed: unknown = JSON.parse(trimmed);
            if (Array.isArray(parsed) && parsed.every((item) => typeof item === 'string')) {
                for (const item of parsed) add(item);
                return { keys, duplicated };
            }
        } catch {
            // 不是合法 JSON 数组时回退到普通文本分隔。
        }
    }

    const lines = content.replace(/\r\n/g, '\n').split('\n');
    for (const rawLine of lines) {
        const line = rawLine.trim();
        if (!line) continue;
        const parts = /[,;\t]/.test(line)
            ? line.split(/[,;\t]/)
            : (/^Bearer\s+/i.test(line) ? [line] : line.split(/\s+/));
        for (const part of parts) add(part);
    }

    return { keys, duplicated };
}
