const process = require("process");

module.exports = async function (context) {
  let bodyParserLimit = process.env.BODY_PARSER_LIMIT || "1mb";
  return {
    status: 200,
    body: bodyParserLimit,
  };
};
