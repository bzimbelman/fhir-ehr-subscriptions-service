import { handlers } from "@/lib/auth";

// NextAuth v5 exposes both GET and POST handlers from a single import. This
// route file wires them into the Next.js App Router.
export const { GET, POST } = handlers;
