import { cookies } from "next/headers";
import { redirect } from "next/navigation";

const apiBase = () => process.env.ADMIN_API_BASE_URL?.trim() || "http://127.0.0.1:8080";

export async function adminApi<T>(path: string, init: RequestInit = {}): Promise<T | null> {
  const token = (await cookies()).get("sokalens_admin_session")?.value;
  if (!token) return null;
  const response = await fetch(`${apiBase()}${path}`, {
    ...init,
    headers: { ...init.headers, Authorization: `Bearer ${token}` },
    cache: "no-store",
    signal: AbortSignal.timeout(8000),
  }).catch(() => null);
  if (response?.status === 401) {
    redirect("/login?reason=session-expired");
  }
  if (!response?.ok) return null;
  return (await response.json()) as T;
}
