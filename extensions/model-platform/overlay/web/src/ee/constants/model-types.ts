export type ModelReferenceKind = 'dataset' | 'assistant' | 'none';
export type ModelMetricUnit = 'tokens' | 'characters';

export type ManagedModelTypeSpec = {
  bit: number;
  key: 'chat' | 'embedding' | 'asr' | 'vision' | 'rerank' | 'tts' | 'decision';
  refKind: ModelReferenceKind;
  metricUnit: ModelMetricUnit;
};

/**
 * Single source of truth for platform-managed model types on the frontend.
 * Mirrors backend/entity/model_slots.go (all non-OCR RAGFlow ModelType bit flags).
 * OCR (64) is deliberately excluded because it runs locally rather than via LiteLLM.
 */
export const MANAGED_MODEL_TYPES: readonly ManagedModelTypeSpec[] = [
  { bit: 1, key: 'chat', refKind: 'assistant', metricUnit: 'tokens' },
  { bit: 2, key: 'embedding', refKind: 'dataset', metricUnit: 'tokens' },
  { bit: 16, key: 'rerank', refKind: 'none', metricUnit: 'tokens' },
  { bit: 4, key: 'asr', refKind: 'none', metricUnit: 'tokens' },
  { bit: 8, key: 'vision', refKind: 'none', metricUnit: 'tokens' },
  { bit: 32, key: 'tts', refKind: 'none', metricUnit: 'characters' },
  { bit: 128, key: 'decision', refKind: 'assistant', metricUnit: 'tokens' },
] as const;

export const MANAGED_MODEL_TYPE_BY_BIT: Readonly<
  Record<number, ManagedModelTypeSpec>
> = Object.fromEntries(MANAGED_MODEL_TYPES.map((spec) => [spec.bit, spec]));

export function getModelTypeSpec(
  bit: number,
): ManagedModelTypeSpec | undefined {
  return MANAGED_MODEL_TYPE_BY_BIT[bit];
}
