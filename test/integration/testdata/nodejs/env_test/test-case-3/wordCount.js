module.exports = async (context) => {
  // node-env registers express.text({ type: "text/*" }), so a text/plain
  // POST body arrives as a string on context.request.body.
  const body = context.request.body;
  const text = typeof body === "string" ? body : "";
  const words = text.trim().split(/\s+/).filter(Boolean);

  return {
    status: 200,
    body: String(words.length),
  };
};
