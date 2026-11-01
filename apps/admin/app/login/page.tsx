import { LoginForm } from "./LoginForm";

export const metadata = {
  title: "Admin sign in | SokaLens",
  robots: { index: false, follow: false },
};

export default function AdminLoginPage() {
  return <LoginForm />;
}

