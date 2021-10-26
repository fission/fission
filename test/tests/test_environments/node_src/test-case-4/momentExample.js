const momentpackage = require("moment");

module.exports = async (context) => {
  return {
    status: 200,
    body: "Hello " + momentpackage().format(),
  };
};
