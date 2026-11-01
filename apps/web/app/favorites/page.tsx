import { AccountPageFrame } from "../components/account/AccountPageFrame";
import { FavoritesList } from "../components/account/FavoritesList";

export const metadata = {
  title: "Favorites - SokaLens",
  robots: { index: false, follow: false },
};

export default function FavoritesPage() {
  return (
    <AccountPageFrame
      kicker="Favorites"
      summary="Keep the teams, leagues, and matches you follow in one focused view."
      title="Your saved football"
    >
      <FavoritesList />
    </AccountPageFrame>
  );
}
