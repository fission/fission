const process = require("process");

module.exports = async function (context) {
  let message =
    "BODY_PARSER_LIMIT received from env variable " +
    process.env.BODY_PARSER_LIMIT;
  return {
    status: 200,
    body: message,
  };
};
