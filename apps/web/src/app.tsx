import { getCoreLiveness } from "@agentos/api-client";
import { Button } from "@agentos/ui/button";
import { useState } from "react";

type Connection = "idle" | "checking" | "connected" | "failed";
const labels: Record<Connection, string> = {
  idle: "尚未检查连接",
  checking: "正在检查连接…",
  connected: "核心进程可达；不代表业务就绪",
  failed: "连接检查失败，请检查核心服务与响应契约",
};

export function App() {
  const [connection, setConnection] = useState<Connection>("idle");
  const [dark, setDark] = useState(false);

  async function checkConnection() {
    setConnection("checking");
    try {
      await getCoreLiveness(AbortSignal.timeout(5000));
      setConnection("connected");
    } catch {
      setConnection("failed");
    }
  }

  function toggleTheme() {
    document.documentElement.classList.toggle("dark", !dark);
    setDark(!dark);
  }

  return (
    <div className="min-h-screen">
      <header className="border-b">
        <div className="mx-auto flex max-w-5xl items-center justify-between px-6 py-5">
          <span className="font-semibold tracking-tight">
            AgentOS{" "}
            <span className="ml-3 font-normal text-muted-foreground">
              / 工作台
            </span>
          </span>
          <Button variant="outline" onClick={toggleTheme} aria-pressed={dark}>
            深色模式
          </Button>
        </div>
      </header>
      <main className="mx-auto max-w-5xl px-6 py-16">
        <p className="mb-4 text-xs font-medium uppercase tracking-widest text-muted-foreground">
          M1 / Engineering foundation
        </p>
        <h1 className="text-3xl font-semibold tracking-tight sm:text-4xl">
          企业级数字员工运行底座
        </h1>
        <p className="mt-4 max-w-2xl text-base leading-7 text-muted-foreground">
          工程骨架已建立。以统一契约连接控制台与核心服务，逐步实现可授权、可恢复、可审计的业务执行。
        </p>
        <section
          aria-labelledby="connection-title"
          className="mt-10 rounded-lg border p-6"
        >
          <div className="flex flex-wrap items-center justify-between gap-5">
            <div>
              <h2 id="connection-title" className="font-medium">
                核心服务连接
              </h2>
              <p
                role="status"
                className={`mt-2 text-sm ${connection === "failed" ? "text-destructive" : "text-muted-foreground"}`}
              >
                {labels[connection]}
              </p>
            </div>
            <Button
              onClick={checkConnection}
              disabled={connection === "checking"}
            >
              检查连接
            </Button>
          </div>
        </section>
        <section aria-labelledby="scope-title" className="mt-8">
          <h2 id="scope-title" className="text-sm font-medium">
            当前范围
          </h2>
          <ul className="mt-3 space-y-2 text-sm text-muted-foreground">
            <li>已建立 · Monorepo、共享组件、静态交付与基础检查</li>
            <li>待接入 · PostgreSQL、身份授权、飞书网关与模型路由</li>
          </ul>
        </section>
      </main>
    </div>
  );
}
