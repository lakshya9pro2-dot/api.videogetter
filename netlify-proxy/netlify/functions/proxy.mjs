/**
 * Netlify Functions V2 Entrypoint for Playback Proxy.
 */

import handler from "../../src/index.mjs";

export default async (request, context) => {
  return handler.fetch(request, process.env, context);
};

export const config = {
  path: "/*",
  preferStatic: false
};
