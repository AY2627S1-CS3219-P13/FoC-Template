import { cookies } from "next/headers";
import { notFound } from "next/navigation";
import AdminConsole from "./admin-console";
import type { User } from "@/lib/user-api";

export const metadata = { title: "Admin | Friend on Campus" };

export default async function AdminPage() {
  const jar = await cookies();
  const session = jar.get("__Host-foc_session") ?? jar.get("foc_session");
  if (!session) notFound();

  let user: User;
  try {
    const response = await fetch(`${process.env.USER_SERVICE_URL ?? "http://user-service:8080"}/api/v1/users/me`, {
      headers: { Cookie: `${session.name}=${session.value}` },
      cache: "no-store",
      signal: AbortSignal.timeout(5000),
    });
    if (!response.ok) notFound();
    ({ user } = (await response.json()) as { user: User });
  } catch {
    notFound();
  }
  if (!user.roles.includes("admin")) notFound();
  return <AdminConsole admin={user} />;
}
