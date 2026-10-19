import schema from "@agentos/contracts/health.json";
import Ajv from "ajv";

export interface CoreLiveness {
  status: "ok";
}

const validate = new Ajv().compile<CoreLiveness>(schema);

export async function getCoreLiveness(
  signal?: AbortSignal,
): Promise<CoreLiveness> {
  const response = await fetch("/api/health", signal ? { signal } : {});
  if (!response.ok) {
    throw new Error(`Core liveness request failed: HTTP ${response.status}`);
  }
  const result: unknown = await response.json();
  if (!validate(result)) {
    throw new Error("Core liveness response violates the contract");
  }
  return result;
}
