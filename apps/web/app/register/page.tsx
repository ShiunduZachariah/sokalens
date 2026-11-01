import { AuthForm } from "../components/account/AuthForm";
import { localizedAuthMetadata } from "../i18n/metadata";

export async function generateMetadata() { return localizedAuthMetadata("auth.register.title", "auth.register.summary", "/register"); }

export default async function RegisterPage({ searchParams }: { searchParams: Promise<{ next?: string }> }) {
  const { next } = await searchParams;
  return <AuthForm mode="register" nextPath={next} />;
}
