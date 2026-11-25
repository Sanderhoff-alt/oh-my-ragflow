import enPrivate from './en-private';
import zhPrivate from './zh-private';
import zhTraditionalPrivate from './zh-traditional-private';

function collectLeafEntries(
  obj: Record<string, unknown>,
  prefix = '',
): Record<string, string> {
  const result: Record<string, string> = {};
  for (const [key, value] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${key}` : key;
    if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
      Object.assign(
        result,
        collectLeafEntries(value as Record<string, unknown>, path),
      );
    } else {
      result[path] = String(value);
    }
  }
  return result;
}

function extractPlaceholders(template: string): string[] {
  const matches = template.match(/\{\{\s*[\w.-]+\s*\}\}/g) ?? [];
  return matches
    .map((m) => m.replace(/^\{\{\s*|\s*\}\}$/g, ''))
    .sort((a, b) => a.localeCompare(b));
}

describe('model-platform private locales consistency', () => {
  const enEntries = collectLeafEntries(enPrivate.translation);
  const enKeys = Object.keys(enEntries).sort();

  const locales: Array<[string, Record<string, unknown>]> = [
    ['zh-private', zhPrivate.translation],
    ['zh-traditional-private', zhTraditionalPrivate.translation],
  ];

  it.each(locales)(
    '%s defines the exact same key set as en-private',
    (_name, translation) => {
      const targetKeys = Object.keys(collectLeafEntries(translation)).sort();
      expect(targetKeys).toEqual(enKeys);
    },
  );

  it.each(locales)(
    '%s preserves all interpolation placeholders from en-private',
    (_name, translation) => {
      const targetEntries = collectLeafEntries(translation);
      for (const key of enKeys) {
        expect(extractPlaceholders(targetEntries[key])).toEqual(
          extractPlaceholders(enEntries[key]),
        );
      }
    },
  );
});
