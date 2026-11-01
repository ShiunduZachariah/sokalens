import { AuthForm } from "../components/account/AuthForm";
import { localizedAuthMetadata } from "../i18n/metadata";

export async function generateMetadata() { return localizedAuthMetadata("auth.login.title", "auth.login.summary", "/login"); }

export default async function LoginPage({ searchParams }: { searchParams: Promise<{ next?: string }> }) {
  const { next } = await searchParams;
  return <AuthForm mode="login" nextPath={next} />;
}
