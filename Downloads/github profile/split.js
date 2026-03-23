const fs = require('fs');

const content = fs.readFileSync('preview.html', 'utf8');

// Match <style>...</style>
const styleMatch = content.match(/<style>([\s\S]*?)<\/style>/);
if (styleMatch) {
  fs.writeFileSync('style.css', styleMatch[1].trim() + '\n');
}

// Match <script>...</script> (only the last one typically contains the app logic)
const scriptMatch = content.match(/<script>([\s\S]*?)<\/script>[\s\S]*?<\/body>/);
if (scriptMatch) {
  fs.writeFileSync('script.js', scriptMatch[1].trim() + '\n');
}

// Replace in HTML
let newHtml = content.replace(/<style>[\s\S]*?<\/style>/, '<link rel="stylesheet" href="style.css">');
newHtml = newHtml.replace(/<script>([\s\S]*?)<\/script>([\s\S]*?<\/body>)/, '<script src="script.js"></script>$2');

fs.writeFileSync('index.html', newHtml);
console.log("Files successfully split into index.html, style.css, and script.js!");
