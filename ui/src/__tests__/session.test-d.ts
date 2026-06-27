import { describe, it, expectTypeOf } from "vitest";
import type { Session } from "next-auth";

describe("Session type augmentation", () => {
  it("includes accessToken, idToken, and user.username", () => {
    expectTypeOf<Session["accessToken"]>().toEqualTypeOf<string | undefined>();
    expectTypeOf<Session["idToken"]>().toEqualTypeOf<string | undefined>();
    expectTypeOf<Session["user"]>().toMatchTypeOf<{
      username?: string;
    }>();
  });
});
