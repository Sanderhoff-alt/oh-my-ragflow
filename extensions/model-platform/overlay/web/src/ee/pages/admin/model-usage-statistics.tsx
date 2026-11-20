import {
  LucideBarChart3,
  LucideCoins,
  LucideFlame,
  LucideTrendingUp,
  LucideClock,
} from 'lucide-react';

export default function ModelUsageStatisticsPage() {
  return (
    <div className="p-8 max-w-7xl mx-auto space-y-6">
      <div className="flex items-center justify-between border-b pb-4">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">
            Model Usage Statistics
          </h1>
          <p className="text-sm text-muted-foreground mt-1">
            Aggregated token consumption, tenant spend audit, and reconciliation
            status.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <span className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-semibold bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300 border border-amber-200 dark:border-amber-800">
            <LucideClock className="size-3.5" />
            Pipeline Status: Pending Backend Integration
          </span>
        </div>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-4 gap-6">
        <div className="p-6 rounded-xl border bg-card text-card-foreground shadow-sm">
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium text-muted-foreground">
              Total Incurred Spend
            </span>
            <LucideCoins className="size-5 text-amber-500" />
          </div>
          <div className="text-2xl font-bold mt-2">—</div>
          <p className="text-xs text-muted-foreground mt-1">
            Placeholder (Awaiting usage pipeline)
          </p>
        </div>

        <div className="p-6 rounded-xl border bg-card text-card-foreground shadow-sm">
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium text-muted-foreground">
              Prompt Tokens
            </span>
            <LucideBarChart3 className="size-5 text-blue-500" />
          </div>
          <div className="text-2xl font-bold mt-2">—</div>
          <p className="text-xs text-muted-foreground mt-1">
            Placeholder (Awaiting usage pipeline)
          </p>
        </div>

        <div className="p-6 rounded-xl border bg-card text-card-foreground shadow-sm">
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium text-muted-foreground">
              Completion Tokens
            </span>
            <LucideFlame className="size-5 text-orange-500" />
          </div>
          <div className="text-2xl font-bold mt-2">—</div>
          <p className="text-xs text-muted-foreground mt-1">
            Placeholder (Awaiting usage pipeline)
          </p>
        </div>

        <div className="p-6 rounded-xl border bg-card text-card-foreground shadow-sm">
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium text-muted-foreground">
              Tenant Self-Managed
            </span>
            <LucideTrendingUp className="size-5 text-emerald-500" />
          </div>
          <div className="text-2xl font-bold mt-2">Zero-Billed</div>
          <p className="text-xs text-muted-foreground mt-1">
            BYO model instances isolated
          </p>
        </div>
      </div>

      <div className="rounded-xl border bg-card text-card-foreground shadow-sm overflow-hidden">
        <div className="p-6 border-b flex items-center justify-between">
          <div>
            <h2 className="text-lg font-semibold">
              Tenant Consumption Breakdown
            </h2>
            <p className="text-sm text-muted-foreground">
              Daily reconciled usage logs across tenants.
            </p>
          </div>
          <span className="px-2.5 py-0.5 rounded text-xs font-medium bg-muted text-muted-foreground border">
            Scaffold Seam
          </span>
        </div>
        <div className="p-6 text-center text-sm text-muted-foreground py-12">
          Usage data unavailable. Awaiting backend usage pipeline integration
          (TASK-4.1 - TASK-4.4).
        </div>
      </div>
    </div>
  );
}
