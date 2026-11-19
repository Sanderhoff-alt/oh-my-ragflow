import { LucideBox, LucideShieldCheck, LucideActivity, LucideClock } from 'lucide-react';

export default function ModelProvidersPage() {
  return (
    <div className="p-8 max-w-7xl mx-auto space-y-6">
      <div className="flex items-center justify-between border-b pb-4">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Model Providers</h1>
          <p className="text-sm text-muted-foreground mt-1">
            Platform-managed model providers, instances, and gateway routing configurations.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <span className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-semibold bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300 border border-amber-200 dark:border-amber-800">
            <LucideClock className="size-3.5" />
            Gateway Status: Pending Integration
          </span>
        </div>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
        <div className="p-6 rounded-xl border bg-card text-card-foreground shadow-sm">
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium text-muted-foreground">Managed Models</span>
            <LucideShieldCheck className="size-5 text-blue-500" />
          </div>
          <div className="text-2xl font-bold mt-2">Platform Pool</div>
          <p className="text-xs text-muted-foreground mt-1">Chat &amp; Embedding models (Preview)</p>
        </div>

        <div className="p-6 rounded-xl border bg-card text-card-foreground shadow-sm">
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium text-muted-foreground">Gateway Engine</span>
            <LucideBox className="size-5 text-indigo-500" />
          </div>
          <div className="text-2xl font-bold mt-2">LiteLLM Proxy</div>
          <p className="text-xs text-muted-foreground mt-1">Topology: Internal isolated (Awaiting connection)</p>
        </div>

        <div className="p-6 rounded-xl border bg-card text-card-foreground shadow-sm">
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium text-muted-foreground">Tenant Protection</span>
            <LucideActivity className="size-5 text-emerald-500" />
          </div>
          <div className="text-2xl font-bold mt-2">403 Guard (Active)</div>
          <p className="text-xs text-muted-foreground mt-1">Backend guard active; UI telemetry pending</p>
        </div>
      </div>

      <div className="rounded-xl border bg-card text-card-foreground shadow-sm overflow-hidden">
        <div className="p-6 border-b flex items-center justify-between">
          <div>
            <h2 className="text-lg font-semibold">Configured Platform Providers</h2>
            <p className="text-sm text-muted-foreground">
              Sample provider configuration for initial scaffold verification.
            </p>
          </div>
          <span className="px-2.5 py-0.5 rounded text-xs font-medium bg-muted text-muted-foreground border">
            Sample Preview Data
          </span>
        </div>
        <div className="p-6">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b text-left text-muted-foreground">
                <th className="pb-3 font-medium">Provider</th>
                <th className="pb-3 font-medium">Instance</th>
                <th className="pb-3 font-medium">Type</th>
                <th className="pb-3 font-medium">Status</th>
                <th className="pb-3 font-medium">Protection</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              <tr>
                <td className="py-3 font-medium">OpenAI-API-Compatible</td>
                <td className="py-3 text-muted-foreground">platform</td>
                <td className="py-3">Managed Gateway</td>
                <td className="py-3">
                  <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300">
                    Awaiting Gateway
                  </span>
                </td>
                <td className="py-3 text-muted-foreground">Protected (403 Guard Active)</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}
